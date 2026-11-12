package aws

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/smithy-go"
)

func wireConfig(t *testing.T, handler http.HandlerFunc) sdk.Config {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("missing actual SDK signature")
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return sdk.Config{Region: "us-east-1", BaseEndpoint: sdk.String(server.URL), HTTPClient: server.Client(), RetryMaxAttempts: 1,
		Credentials: sdk.CredentialsProviderFunc(func(context.Context) (sdk.Credentials, error) {
			return sdk.Credentials{AccessKeyID: "test-access", SecretAccessKey: "test-secret", SessionToken: "test-session"}, nil
		})}
}
func readJSON(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var in map[string]any
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		t.Error(err)
	}
	return in
}
func respond(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	_ = json.NewEncoder(w).Encode(body)
}
func field(t *testing.T, in map[string]any, key string, want any) {
	t.Helper()
	if !reflect.DeepEqual(in[key], want) {
		t.Errorf("%s = %#v, want %#v", key, in[key], want)
	}
}

func TestSQSReceiptDispatchAndError(t *testing.T) {
	var mu sync.Mutex
	calls := []string{}
	cfg := wireConfig(t, func(w http.ResponseWriter, r *http.Request) {
		action := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonSQS.")
		in := readJSON(t, r)
		mu.Lock()
		calls = append(calls, action)
		mu.Unlock()
		switch action {
		case "SendMessage":
			field(t, in, "DelaySeconds", float64(12))
			field(t, in, "MessageBody", "work")
			sum := md5.Sum([]byte("work"))
			respond(w, map[string]any{"MessageId": "m", "MD5OfMessageBody": hex.EncodeToString(sum[:])})
		case "ReceiveMessage":
			respond(w, map[string]any{"Messages": []any{map[string]any{"MessageId": "m", "Body": "work", "ReceiptHandle": "original-receipt"}}})
		case "DeleteMessage":
			field(t, in, "ReceiptHandle", "original-receipt")
			respond(w, map[string]any{})
		case "ChangeMessageVisibility":
			field(t, in, "ReceiptHandle", "original-receipt")
			field(t, in, "VisibilityTimeout", float64(0))
			w.Header().Set("Content-Type", "application/x-amz-json-1.0")
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"__type":"ReceiptHandleIsInvalid","message":"expired"}`)
		default:
			t.Errorf("unexpected action %s", action)
		}
	})
	a := SQS{Client: sqs.NewFromConfig(cfg)}
	ctx := context.Background()
	url := "https://sqs.us-east-1.amazonaws.com/123/work"
	input := &sqs.SendMessageInput{QueueUrl: &url, MessageBody: sdk.String("work")}
	if _, err := a.Dispatch(ctx, input, 12*time.Second); err != nil {
		t.Fatal(err)
	}
	if input.DelaySeconds != 0 {
		t.Fatal("input mutated")
	}
	deliveries, err := a.Receive(ctx, &sqs.ReceiveMessageInput{QueueUrl: &url, WaitTimeSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	count := len(calls)
	mu.Unlock()
	if count != 2 {
		t.Fatal("receive auto-acked")
	}
	*deliveries[0].Message.ReceiptHandle = "tampered"
	if err := deliveries[0].Ack(ctx); err != nil {
		t.Fatal(err)
	}
	var api smithy.APIError
	if err := deliveries[0].Nack(ctx, 0); !errors.As(err, &api) || api.ErrorCode() != "ReceiptHandleIsInvalid" {
		t.Fatalf("lost provider error %v", err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := deliveries[0].Ack(cancelCtx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, delay := range []time.Duration{-1, time.Millisecond, 901 * time.Second} {
		if _, err := a.Dispatch(ctx, input, delay); err == nil {
			t.Fatal("invalid delay accepted")
		}
	}
	fifo := *input
	fifo.QueueUrl = sdk.String(url + ".fifo")
	if _, err := a.Dispatch(ctx, &fifo, time.Second); err == nil {
		t.Fatal("FIFO delay accepted")
	}
	if err := deliveries[0].Nack(ctx, 13*time.Hour); err == nil {
		t.Fatal("invalid visibility")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 4 {
		t.Fatalf("unexpected calls %v", calls)
	}
}

func TestSNSPublishNativeQuery(t *testing.T) {
	cfg := wireConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		for key, want := range map[string]string{"Action": "Publish", "TopicArn": "arn:aws:sns:us-east-1:123:events", "Message": "{\"id\":\"stable\"}", "MessageGroupId": "root", "MessageDeduplicationId": "stable"} {
			if r.Form.Get(key) != want {
				t.Errorf("%s: %s", key, r.Form.Get(key))
			}
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, `<PublishResponse xmlns="http://sns.amazonaws.com/doc/2010-03-31/"><PublishResult><MessageId>published</MessageId></PublishResult><ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></PublishResponse>`)
	})
	out, err := (SNS{Client: sns.NewFromConfig(cfg)}).Publish(context.Background(), &sns.PublishInput{TopicArn: sdk.String("arn:aws:sns:us-east-1:123:events"), Message: sdk.String(`{"id":"stable"}`), MessageGroupId: sdk.String("root"), MessageDeduplicationId: sdk.String("stable")})
	if err != nil || sdk.ToString(out.MessageId) != "published" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestSchedulerNativeTarget(t *testing.T) {
	cfg := wireConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/schedules/daily" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		in := readJSON(t, r)
		field(t, in, "ScheduleExpression", "cron(0 9 * * ? *)")
		field(t, in, "ScheduleExpressionTimezone", "America/Sao_Paulo")
		field(t, in, "ClientToken", "stable")
		target := in["Target"].(map[string]any)
		field(t, target, "Arn", "arn:aws:sqs:us-east-1:123:work")
		field(t, target, "RoleArn", "arn:aws:iam::123:role/scheduler")
		field(t, target, "Input", `{"task":"run"}`)
		respond(w, map[string]any{"ScheduleArn": "arn:aws:scheduler:us-east-1:123:schedule/default/daily"})
	})
	a := Scheduler{Client: scheduler.NewFromConfig(cfg)}
	_, err := a.CreateSchedule(context.Background(), &scheduler.CreateScheduleInput{Name: sdk.String("daily"), ClientToken: sdk.String("stable"), ScheduleExpression: sdk.String("cron(0 9 * * ? *)"), ScheduleExpressionTimezone: sdk.String("America/Sao_Paulo"), FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff}, Target: &schedtypes.Target{Arn: sdk.String("arn:aws:sqs:us-east-1:123:work"), RoleArn: sdk.String("arn:aws:iam::123:role/scheduler"), Input: sdk.String(`{"task":"run"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateSchedule(context.Background(), &scheduler.CreateScheduleInput{Name: sdk.String("missing-target")}); err == nil {
		t.Fatal("missing native target accepted")
	}
}

func TestKMSNativeCryptoAndES256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	keyID := "arn:aws:kms:us-east-1:123:key/immutable"
	message := []byte("header.payload")
	digest := sha256.Sum256(message)
	cfg := wireConfig(t, func(w http.ResponseWriter, r *http.Request) {
		in := readJSON(t, r)
		switch strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "TrentService.") {
		case "GetPublicKey":
			respond(w, map[string]any{"KeyId": keyID, "KeySpec": "ECC_NIST_P256", "KeyUsage": "SIGN_VERIFY", "PublicKey": der, "SigningAlgorithms": []string{"ECDSA_SHA_256"}})
		case "Sign":
			field(t, in, "KeyId", keyID)
			field(t, in, "MessageType", "DIGEST")
			field(t, in, "SigningAlgorithm", "ECDSA_SHA_256")
			field(t, in, "Message", base64.StdEncoding.EncodeToString(digest[:]))
			signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
			if err != nil {
				t.Error(err)
			}
			respond(w, map[string]any{"KeyId": keyID, "SigningAlgorithm": "ECDSA_SHA_256", "Signature": signature})
		case "Verify":
			field(t, in, "SigningAlgorithm", "RSASSA_PSS_SHA_384")
			field(t, in, "MessageType", "RAW")
			field(t, in, "Message", base64.StdEncoding.EncodeToString([]byte{0, 255, 1}))
			respond(w, map[string]any{"KeyId": keyID, "SignatureValid": true, "SigningAlgorithm": "RSASSA_PSS_SHA_384"})
		case "Encrypt":
			field(t, in, "EncryptionContext", map[string]any{"tenant": "a"})
			field(t, in, "Plaintext", base64.StdEncoding.EncodeToString([]byte{0, 255, 1}))
			field(t, in, "EncryptionAlgorithm", "SYMMETRIC_DEFAULT")
			respond(w, map[string]any{"CiphertextBlob": []byte{4, 0, 255}, "KeyId": keyID})
		case "Decrypt":
			field(t, in, "CiphertextBlob", base64.StdEncoding.EncodeToString([]byte{4, 0, 255}))
			field(t, in, "EncryptionContext", map[string]any{"tenant": "a"})
			respond(w, map[string]any{"Plaintext": []byte{0, 255, 1}, "KeyId": keyID})
		default:
			t.Error("unexpected KMS operation")
		}
	})
	a := KMS{Client: kms.NewFromConfig(cfg)}
	ctx := context.Background()
	signature, err := a.SignES256(ctx, "alias/signing", message)
	if err != nil {
		t.Fatal(err)
	}
	if len(signature) != 64 {
		t.Fatal("not JOSE")
	}
	if err := a.VerifyES256(ctx, keyID, message, signature); err != nil {
		t.Fatal(err)
	}
	if err := a.VerifyES256(ctx, keyID, []byte("tampered"), signature); err == nil {
		t.Fatal("tamper accepted")
	}
	if _, err := a.SignES256(ctx, "", message); err == nil {
		t.Fatal("empty key accepted")
	}
	verify, err := a.Verify(ctx, &kms.VerifyInput{KeyId: &keyID, Message: []byte{0, 255, 1}, Signature: []byte{1, 2}, MessageType: kmstypes.MessageTypeRaw, SigningAlgorithm: kmstypes.SigningAlgorithmSpecRsassaPssSha384})
	if err != nil || !verify.SignatureValid {
		t.Fatal(err)
	}
	encrypted, err := a.Encrypt(ctx, &kms.EncryptInput{KeyId: &keyID, Plaintext: []byte{0, 255, 1}, EncryptionContext: map[string]string{"tenant": "a"}, EncryptionAlgorithm: kmstypes.EncryptionAlgorithmSpecSymmetricDefault})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := a.Decrypt(ctx, &kms.DecryptInput{KeyId: &keyID, CiphertextBlob: encrypted.CiphertextBlob, EncryptionContext: map[string]string{"tenant": "a"}})
	if err != nil || !bytes.Equal(plain.Plaintext, []byte{0, 255, 1}) {
		t.Fatalf("%+v %v", plain, err)
	}
}

func TestSecretsPreserveBinary(t *testing.T) {
	data := []byte{0, 255, 0, 32, 10}
	cfg := wireConfig(t, func(w http.ResponseWriter, r *http.Request) {
		in := readJSON(t, r)
		switch strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "secretsmanager.") {
		case "CreateSecret":
			field(t, in, "SecretBinary", base64.StdEncoding.EncodeToString(data))
			field(t, in, "ClientRequestToken", "12345678-1234-1234-1234-123456789012")
			respond(w, map[string]any{"Name": "binary", "ARN": "arn:aws:secretsmanager:us-east-1:123:secret:binary"})
		case "GetSecretValue":
			field(t, in, "VersionStage", "AWSCURRENT")
			respond(w, map[string]any{"SecretBinary": data, "VersionId": "v1"})
		default:
			t.Error("unexpected secrets operation")
		}
	})
	a := Secrets{Client: secretsmanager.NewFromConfig(cfg)}
	ctx := context.Background()
	if _, err := a.CreateSecret(ctx, &secretsmanager.CreateSecretInput{Name: sdk.String("binary"), SecretBinary: data, ClientRequestToken: sdk.String("12345678-1234-1234-1234-123456789012")}); err != nil {
		t.Fatal(err)
	}
	out, err := a.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: sdk.String("binary"), VersionStage: sdk.String("AWSCURRENT")})
	if err != nil || !bytes.Equal(out.SecretBinary, data) {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestStoragePaginationAndBytes(t *testing.T) {
	data := []byte{0, 255, 32, 10}
	var pages int
	cfg := wireConfig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PUT":
			if r.URL.Path != "/roles/a%20b" && r.URL.Path != "/roles/a b" {
				t.Error(r.URL.Path)
			}
			got, _ := io.ReadAll(r.Body)
			if !bytes.Equal(got, data) {
				t.Errorf("put bytes %x", got)
			}
			w.Header().Set("ETag", `"etag"`)
		case r.URL.Query().Get("list-type") == "2":
			pages++
			w.Header().Set("Content-Type", "application/xml")
			if pages == 1 {
				_, _ = io.WriteString(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next/+</NextContinuationToken><Contents><Key>first</Key></Contents></ListBucketResult>`)
			} else {
				if r.URL.Query().Get("continuation-token") != "next/+" {
					t.Error("lost continuation token")
				}
				_, _ = io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>second</Key></Contents></ListBucketResult>`)
			}
		case r.Method == "GET":
			_, _ = w.Write(data)
		default:
			t.Error("unexpected S3 operation")
		}
	})
	a := Storage{Client: s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.RequestChecksumCalculation = sdk.RequestChecksumCalculationWhenRequired
	})}
	ctx := context.Background()
	if _, err := a.PutObject(ctx, &s3.PutObjectInput{Bucket: sdk.String("roles"), Key: sdk.String("a b"), Body: bytes.NewReader(data)}); err != nil {
		t.Fatal(err)
	}
	out, err := a.GetObject(ctx, &s3.GetObjectInput{Bucket: sdk.String("roles"), Key: sdk.String("a b")})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(out.Body)
	_ = out.Body.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal(err)
	}
	var keys []string
	input := &s3.ListObjectsV2Input{Bucket: sdk.String("roles")}
	if err := a.WalkObjects(ctx, input, func(o s3types.Object) error { keys = append(keys, sdk.ToString(o.Key)); return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, []string{"first", "second"}) || input.ContinuationToken != nil {
		t.Fatalf("%v %+v", keys, input)
	}
}

func TestLogsPartialRejection(t *testing.T) {
	cfg := wireConfig(t, func(w http.ResponseWriter, r *http.Request) {
		in := readJSON(t, r)
		field(t, in, "logGroupName", "group")
		field(t, in, "logStreamName", "stream")
		events := in["logEvents"].([]any)
		field(t, events[0].(map[string]any), "message", `{"severity":"ERROR","id":"stable"}`)
		respond(w, map[string]any{"rejectedLogEventsInfo": map[string]any{"tooOldLogEventEndIndex": 0}})
	})
	out, err := (Logs{Client: cloudwatchlogs.NewFromConfig(cfg)}).Write(context.Background(), &cloudwatchlogs.PutLogEventsInput{LogGroupName: sdk.String("group"), LogStreamName: sdk.String("stream"), LogEvents: []logtypes.InputLogEvent{{Message: sdk.String(`{"severity":"ERROR","id":"stable"}`), Timestamp: sdk.Int64(123)}}})
	if err == nil || out == nil || out.RejectedLogEventsInfo == nil {
		t.Fatalf("partial rejection swallowed: %+v %v", out, err)
	}
}

func TestServiceAuthActualRequest(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		got, _ := io.ReadAll(r.Body)
		if string(got) != "payload" {
			t.Error("body consumed")
		}
		if r.Header.Get("X-Amz-Security-Token") != "session" {
			t.Error("missing session token")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	req, _ := http.NewRequest("POST", server.URL+"/path?b=2&a=1", strings.NewReader("payload"))
	a := ServiceAuth{Region: "us-east-1", Service: "execute-api", Credentials: sdk.CredentialsProviderFunc(func(context.Context) (sdk.Credentials, error) {
		return sdk.Credentials{AccessKeyID: "test", SecretAccessKey: "secret", SessionToken: "session"}, nil
	})}
	signed, err := a.Sign(context.Background(), req, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("input mutated")
	}
	response, err := server.Client().Do(signed)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !strings.Contains(authHeader, "/us-east-1/execute-api/aws4_request") || !strings.Contains(authHeader, "Signature=") {
		t.Fatal(authHeader)
	}
	broken, _ := http.NewRequest("POST", server.URL, io.NopCloser(strings.NewReader("payload")))
	if _, err := a.Sign(context.Background(), broken, time.Now()); err == nil {
		t.Fatal("nonreplayable body accepted")
	}
	a.Credentials = sdk.CredentialsProviderFunc(func(context.Context) (sdk.Credentials, error) {
		return sdk.Credentials{}, errors.New("credential failure")
	})
	if _, err := a.Sign(context.Background(), req, time.Now()); err == nil {
		t.Fatal("credential error lost")
	}
}

// Interface injection checks malformed SDK output without using fake success as
// protocol evidence. All happy-path evidence above uses real SDK/local HTTP.
type nilReceive struct{ SQSAPI }

func (nilReceive) ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return nil, nil
}
func TestMalformedReceive(t *testing.T) {
	if _, err := (SQS{Client: nilReceive{}}).Receive(context.Background(), &sqs.ReceiveMessageInput{QueueUrl: sdk.String("queue")}); err == nil {
		t.Fatal("nil response accepted")
	}
}

func TestES256RejectsInvalidProviderSignature(t *testing.T) {
	for _, tc := range []struct {
		name           string
		signature      []byte
		algorithm, key string
	}{
		{"malformed DER", []byte{1, 2, 3}, "ECDSA_SHA_256", "resolved"},
		{"wrong algorithm", []byte{}, "ECDSA_SHA_384", "resolved"},
		{"wrong key", []byte{}, "ECDSA_SHA_256", "different"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := wireConfig(t, func(w http.ResponseWriter, r *http.Request) {
				_ = readJSON(t, r)
				if strings.HasSuffix(r.Header.Get("X-Amz-Target"), "GetPublicKey") {
					respond(w, map[string]any{"KeyId": "resolved", "KeySpec": "ECC_NIST_P256", "KeyUsage": "SIGN_VERIFY"})
				} else {
					respond(w, map[string]any{"KeyId": tc.key, "SigningAlgorithm": tc.algorithm, "Signature": tc.signature})
				}
			})
			if _, err := (KMS{Client: kms.NewFromConfig(cfg)}).SignES256(context.Background(), "alias/key", []byte("message")); err == nil {
				t.Fatal("invalid provider signature accepted")
			}
		})
	}
}

func TestWalkObjectsFailures(t *testing.T) {
	for _, mode := range []string{"missing token", "repeated token", "visitor", "provider", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			var calls int
			cfg := wireConfig(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/xml")
				if mode == "provider" {
					w.WriteHeader(403)
					_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>denied</Message></Error>`)
					return
				}
				token := ""
				if mode == "repeated token" {
					token = "<NextContinuationToken>same</NextContinuationToken>"
				}
				_, _ = io.WriteString(w, `<ListBucketResult><IsTruncated>true</IsTruncated>`+token+`<Contents><Key>role</Key></Contents></ListBucketResult>`)
			})
			a := Storage{Client: s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			sentinel := errors.New("visitor failed")
			err := a.WalkObjects(ctx, &s3.ListObjectsV2Input{Bucket: sdk.String("roles")}, func(s3types.Object) error {
				if mode == "visitor" {
					return sentinel
				}
				return nil
			})
			if err == nil {
				t.Fatal("failure swallowed")
			}
			if mode == "visitor" && !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
			if mode == "canceled" && (!errors.Is(err, context.Canceled) || calls != 0) {
				t.Fatal(err, calls)
			}
			if mode == "repeated token" && calls != 2 {
				t.Fatalf("listing calls %d", calls)
			}
		})
	}
}

func TestProviderErrorsAndCancellation(t *testing.T) {
	cfg := wireConfig(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"__type":"AccessDeniedException","message":"denied"}`)
	})
	ctx := context.Background()
	cases := map[string]func(context.Context) error{
		"kms": func(ctx context.Context) error {
			_, err := (KMS{Client: kms.NewFromConfig(cfg)}).GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: sdk.String("key")})
			return err
		},
		"secrets": func(ctx context.Context) error {
			_, err := (Secrets{Client: secretsmanager.NewFromConfig(cfg)}).GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: sdk.String("secret")})
			return err
		},
		"scheduler": func(ctx context.Context) error {
			_, err := (Scheduler{Client: scheduler.NewFromConfig(cfg)}).GetSchedule(ctx, &scheduler.GetScheduleInput{Name: sdk.String("name")})
			return err
		},
		"logs": func(ctx context.Context) error {
			_, err := (Logs{Client: cloudwatchlogs.NewFromConfig(cfg)}).Write(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: sdk.String("g"), LogStreamName: sdk.String("s"), LogEvents: []logtypes.InputLogEvent{{Message: sdk.String("m"), Timestamp: sdk.Int64(1)}}})
			return err
		},
		"sns": func(ctx context.Context) error {
			_, err := (SNS{Client: sns.NewFromConfig(cfg)}).Publish(ctx, &sns.PublishInput{TopicArn: sdk.String("arn:aws:sns:us-east-1:123:events"), Message: sdk.String("m")})
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			if err := call(ctx); err == nil {
				t.Fatal("provider error swallowed")
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if err := call(canceled); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestSQSCancelInFlight(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	cfg := wireConfig(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(finished)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := (SQS{Client: sqs.NewFromConfig(cfg)}).Receive(ctx, &sqs.ReceiveMessageInput{QueueUrl: sdk.String("https://sqs.us-east-1.amazonaws.com/123/work"), WaitTimeSeconds: 20})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("receive never started")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receive failed to cancel")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("server request not canceled")
	}
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }
func TestAuthBodyFailureAndCancellation(t *testing.T) {
	a := ServiceAuth{Region: "us-east-1", Service: "execute-api", Credentials: sdk.CredentialsProviderFunc(func(ctx context.Context) (sdk.Credentials, error) {
		return sdk.Credentials{AccessKeyID: "test", SecretAccessKey: "secret"}, nil
	})}
	for _, failCall := range []int{1, 2} {
		t.Run(string(rune('0'+failCall)), func(t *testing.T) {
			req, _ := http.NewRequest("POST", "https://local.invalid", strings.NewReader("body"))
			calls := 0
			var bodies []*trackedBody
			req.GetBody = func() (io.ReadCloser, error) {
				calls++
				body := &trackedBody{Reader: strings.NewReader("body")}
				bodies = append(bodies, body)
				if calls == failCall {
					return body, errors.New("replay failed")
				}
				return body, nil
			}
			if _, err := a.Sign(context.Background(), req, time.Now()); err == nil {
				t.Fatal("GetBody error lost")
			}
			for _, body := range bodies {
				if !body.closed {
					t.Fatal("leaked replay body")
				}
			}
		})
	}
	req, _ := http.NewRequest("GET", "https://local.invalid", nil)
	ctx, cancel := context.WithCancel(context.Background())
	a.Credentials = sdk.CredentialsProviderFunc(func(context.Context) (sdk.Credentials, error) {
		cancel()
		return sdk.Credentials{AccessKeyID: "test", SecretAccessKey: "secret"}, nil
	})
	if _, err := a.Sign(ctx, req, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
