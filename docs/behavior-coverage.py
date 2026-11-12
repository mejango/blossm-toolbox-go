#!/usr/bin/env python3
"""Refresh/check source-anchored behavioral ledger. Does not run tests or infer parity."""
import argparse
import hashlib
import json
import re
import os
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DIA_SOURCE = Path(os.environ.get("DIA_SOURCE", str(ROOT.parent / "dia-go"))).resolve()
# Verification-only roots. Neither Go module depends on a sibling checkout.
PREFIX_MOVES = {
    'command': 'procedures/command', 'fact': 'procedures/fact',
    'job': 'procedures/job', 'projection': 'procedures/projection',
    'eventstore': 'stores/event', 'view': 'stores/view', 'evm': 'onchain/evm',
    'cryptography': 'security/cryptography', 'audit': 'security/audit',
    'runtimeauth': 'runtime/auth', 'runtimeenv': 'runtime/env',
    'runtimepub': 'runtime/publication', 'runtimequeue': 'runtime/queue',
    'dia': 'dia-go:services/identity', 'wallet': 'dia-go:services/wallet',
    'diaruntime': 'dia-go:runtime', 'serviceapp': 'dia-go:examples/document',
    'cmd/framework': 'dia-go:cmd/document', 'cmd/dia': 'dia-go:cmd/dia',
}
# These owners split individual files/tests; a directory rename is insufficient.
FILE_MOVES = {
    'dia/auth.go': ['dia-go:services/identity/auth.go', 'security/token/jwt.go'],
    'dia/auth_test.go': ['dia-go:services/identity/auth_test.go', 'security/token/jwt_test.go'],
    'dia/signing.go': ['dia-go:services/identity/signing.go', 'security/token/signing.go'],
    'dia/signing_test.go': ['dia-go:services/identity/signing_test.go', 'security/token/signing_test.go'],
    'transport/auth.go': ['transport/auth.go', 'dia-go:httpapi/auth.go'],
    'transport/auth_test.go': ['transport/auth_test.go', 'dia-go:httpapi/auth_test.go'],
    'transport/http.go': ['transport/http.go', 'dia-go:httpapi/http.go'],
    'transport/auth_delegated.go': ['dia-go:httpapi/auth_delegated.go'],
    'transport/auth_delegated_test.go': ['dia-go:httpapi/auth_delegated_test.go'],
    'transport/routes.go': ['dia-go:httpapi/routes.go'],
    'integration/framework_flow_test.go': ['dia-go:integration/framework_flow_test.go'],
    'integration/session_test.go': ['dia-go:integration/session_test.go'],
    'integration/container_test.go': ['integration/container_cli_test.go', 'dia-go:integration/container_test.go'],
    'cli/internal_token_test.go': ['cli/internal_token_test.go', 'dia-go:examples/document/cli_token_test.go'],
}

TEST_REPLACEMENTS = {
    'cli/runtime_test.go:TestFrameworkRuntimePlans': ('TestApplicationRuntimePlans', 'CLI now emits application-owned settings without inventing Dia runtime requirements.'),
    'cli/runtime_test.go:TestFrameworkCheckedExamples': ('TestDeliveryCheckedExamples', 'Generic checked delivery examples replace the Dia-backed framework executable configuration; Dia owns cmd/document configuration tests.'),
    'cli/runtime_test.go:TestFrameworkPeerAuthorityConfiguration': ('TestPeerAuthorityConfiguration', 'Generic peer configuration remains validated without owning Dia identity policy.'),
    'cli/runtime_test.go:TestFrameworkRejectsUnverifiedCloudRunIAMDelivery': ('TestRejectsUnverifiedCloudRunIAMDelivery', 'Same explicit IAM/token-forwarding rejection lives in the application-independent CLI.'),
}

def current_paths(file):
    if file in FILE_MOVES:
        return FILE_MOVES[file]
    for old, new in sorted(PREFIX_MOVES.items(), key=lambda pair: -len(pair[0])):
        if file == old or file.startswith(old + '/'):
            return [new + file[len(old):]]
    return [file]

def source_path(file):
    return DIA_SOURCE / file.removeprefix('dia-go:') if file.startswith('dia-go:') else ROOT / file

def source_link(file, repositories, line=None):
    if file.startswith('dia-go:'):
        repo = repositories['dia-go']
        relative = file.removeprefix('dia-go:')
        kind = 'blob' if Path(relative).suffix else 'tree'
        target = repo['url'] + '/' + kind + '/' + (repo.get('revision') or 'main') + '/' + relative
    else:
        target = '../' + file
    return target + (f'#L{line}' if line else '')

FAMILIES = []

def F(id, packages, contract, go, disposition='assertions-mapped', remaining=''):
    FAMILIES.append(dict(id=id, packages=['@blossm/'+p for p in packages.split()], contract=contract,
                         go_selectors=go.split(';') if go else [], disposition=disposition, remaining=remaining))

# Each row names an observable contract, not an implementation directory.
F('set-membership', 'array-difference array-intersection', 'Differences/intersections preserve membership decisions; role/group additions and removals do not duplicate or remove unrelated grants.', 'dia/service_test.go:TestGroupCreationRetryAndRemovalKeepRolesAligned;dia/identity_race_test.go:TestDynamicRoleGrantExpansionInterleavings', 'deliberate-contract-change', 'Typed grant sets replace generic JS array helpers; reference ordering/duplicate-element helper API is not claimed.')
F('authentication-and-live-revocation', 'authenticate authentication-middleware authorization-middleware authorize verify-access-token deleted-scene-check terminated-session-check role-permissions permissions-lookup', 'Authentication validates actual identity, session/scene state and required authority before effects; revocation takes effect without a stale permission cache.', 'dia/service_test.go:TestSessionLifecycleAndCreationCredential|TestSceneGroupAndRoleAuthority;dia/system_test.go:TestSystemAnonymousNodeAndExpiredParentToken', 'assertions-mapped', 'Gateway protection modes and token transport have their own unresolved family below.')
F('token-families-and-gateway-credentials', 'basic-token connection-token network-token operation-token key-claims node-external-token tokens-from-req', 'Basic/key/contextual service credentials and token extraction must establish the intended subject/audience/operation, with no caller-forged claims.', 'dia/system_test.go:TestSystemConnectionBindingAndKeyRotation;transport/auth_test.go:TestGatewayCredentialModesAndIsolation|TestGatewayDiaSessionAndConnectionAuthority|TestGatewayPinnedServiceJWT;runtimeauth/signing_test.go:TestRuntimeSigningSelection|TestRuntimeSigningRejectsInvalidConfiguration;transport/auth_delegated_test.go:TestDelegatedCommandRealCredentialComposition|TestDelegatedCommandRejectsTamperedAuthority|TestDelegatedCommandExpiryCeiling;integration/framework_flow_test.go:TestGenericFrameworkNativeFlow', 'assertions-mapped', 'Actual signed parent Main -> CommandClient -> child fixtures cover target/peer identity, context and ordered transaction binding, revocation and expiry ceiling. Gateway owner fresh full transport race PASS2.138s and vet PASS on Go1.27.4. Final frozen-source local gates and bounded review are closed. Current native assertions pin copy sourceVersion/history: editing the source before retry preserves the original child duplicate; delegated command credentials reject fact/job/view routes. Final frozen-source local gates and bounded review are closed.')
F('jwt-expiry-signature-authority', 'jwt', 'Reject wrong signature/algorithm/audience/issuer and expired/not-yet-valid credentials; configured keys are authority.', 'dia/auth_test.go:TestJWT', 'deliberate-contract-change', 'Go ES256 epoch-second JWT replaces JS ISO-date/algorithm-flexible wire format; cloud signing composition separately tracked.')
F('chainpoint-handle-binding', 'chainpoint', 'Hash submission and proof retrieval preserve matching handles, reject foreign gateway replies and propagate errors.', 'integrations/chainpoint/client_test.go:TestSubmitAndRetrieve', 'deliberate-contract-change', 'Automatic public-IP discovery explicitly retired; trusted configured HTTPS gateway only. No live network discovery or cryptographic proof-verification claim.')
F('cli-init-scaffold-root', 'cli cli-root-dir init-cli-template normalize-cli http-dependency-cli', 'Init/scaffold refuse overwrite, use one module and delegate to actual owners; generated entry points consume public framework owners and the selected independent application builds without Dia runtime assumptions.', 'cli/workflows_test.go:TestScaffoldsCompileInOneModule;cli/cli_test.go:TestOfflineCLI;cli/application_test.go:TestIndependentApplicationBuild|TestApplicationPaths', 'assertions-mapped', 'Gateway serviceapp.New scaffold, all generated kinds and actual three-binary CLI build verified. CLI race PASS7.110s and vet PASS on Go1.27.4; parent process/container and final review gates separate.')
F('cli-deploy-and-resource-actions', 'deploy-cli-template execute-cli-template merge-cli-template', 'Prepared defaults are fully offline; explicit actions use argv, exact receipts/targets and preserve errors. Secrets stay out of argv/plans.', 'cli/workflows_test.go:TestResourceWorkflowsPrepareAndExecute|TestProviderFailureStopsExecution|TestDeployExecutionUsesRegisteredRevision|TestDeployMalformedReceiptAndFailure|TestDefaultDeployNeverRunsOrReadsSecrets;cli/publication_test.go:TestAWSPublicationSizing;cli/runtime_test.go:TestFrameworkRuntimePlans|TestSigningConfiguration|TestFrameworkCheckedExamples|TestDeliveryConfigRejectsUnsafePlans;cli/application_test.go:TestOptionalInfrastructureAndEnvironmentOwnership|TestPeerURLAndIdentitySecurity;cli/runtime_test.go:TestFrameworkPeerAuthorityConfiguration;cli/replay_test.go:TestReplayRequiresLogicalRunAndValidatedAcknowledgment|TestRejectStaticReplayConfiguration;cli/runtime_test.go:TestCloudRunExplicitInvocationMode;cli/runtime_test.go:TestFrameworkRejectsUnverifiedCloudRunIAMDelivery|TestPlansRequireOperatorRedrivePolicy;cli/replay_test.go:TestOperationURLLoopbackBoundaryBeforeCredentials;cli/runtime_test.go:TestSigningCredentialEnvironmentRejected|TestMongoPhysicalDatabaseIndependentOfNamespace;dia-go:deployment/config_test.go:TestPairedApplicationsShareIdentityAndIsolatePublications', 'assertions-mapped', 'Framework GCP requires application invocation; IAM plus intact Tasks token validation is conservatively unsupported, not proven universally broken. Dia IAM requires authenticating edge. Plain HTTP operations only on localhost/literal loopback; remote HTTPS required. Operator retry/redrive prerequisite explicit, no DLQ provisioning. Full CLI race PASS6.337s and vet PASS; no live changes.')
F('cli-test-build-failures', 'test-cli-template', 'Real child test/build failures and missing native settings propagate, with no fabricated successful test runner.', 'cli/cli_test.go:TestRealGoFailurePropagation|TestSubprocessFailureAndOfflineEnvironment|TestNativePrerequisitesFailClosed')
F('developer-identity', 'get-token', 'Offline preparation never discovers credentials; explicit provider-token acquisition and internal-token signing use configured authority and propagate failures. Internal service JWT has configured subject/network audience and short bounded lifetime.', 'cli/token_test.go:TestDeveloperToken;cli/internal_token_test.go:TestInternalTokenOfflineAndBounds|TestInternalTokenDrivesServiceAppReplay|TestInternalTokenKMSSelectionAndFailures', 'deliberate-contract-change', 'Internal-token uses runtimeauth.Open and JWT.Sign. Actual CLI local mint drives serviceapp replay over loopback HTTP with injected root store/producer; wrong subject/network return401. Local/synthetic KMS tests only; no native DB or live signer evidence. Full CLI race PASS6.337s and vet PASS Go1.27.4; final frozen-source local gates and bounded review closed.')
F('command-validation-trace-nested', 'command command-post validate-command', 'Validate before running user code; root/context/claims/transaction trace and nested command identity remain consistent across services.', 'command/command_test.go:TestValidationOrderAndTrace|TestCommandIDCannotBeRetargeted|TestConditionalConflictDoesNotInvokeCallback;transport/command_client_test.go:TestNestedCommandTwoServices|TestCommandClientFailureAndAuthorityBoundary|TestCommandClientDeadlineNoRetry;transport/auth_delegated_test.go:TestDelegatedCommandRealCredentialComposition|TestDelegatedCommandRejectsTamperedAuthority|TestDelegatedCommandExpiryCeiling;integration/framework_flow_test.go:TestGenericFrameworkNativeFlow', 'assertions-mapped', 'Actual signed parent Main -> CommandClient -> child fixtures cover target/peer identity, context and ordered transaction binding, revocation and expiry ceiling. Gateway owner fresh full transport race PASS2.138s and vet PASS on Go1.27.4. Final frozen-source local gates and bounded review are closed. Current native assertions pin copy sourceVersion/history: editing the source before retry preserves the original child duplicate; delegated command credentials reject fact/job/view routes. Final frozen-source local gates and bounded review are closed.')
F('command-callback-outcomes', 'event-outcome-consts', 'Callbacks receive input-aligned committed/duplicate/queued/empty outcomes; callback-only body/status/tokens/revokes survive duplicate responses without repeated effects.', 'command/command_test.go:TestFreshCommitDuplicateAndCallbackContributions|TestMixedBatchCallbackGetsAllOutcomes|TestCallbackFailurePreservesCommitAndIsNotDurable|TestTokenOnlyResponseHasContent;command/command_test.go:TestThenEmptyAndQueuedOutcomes|TestThenFailurePreservesQueuedAcceptance|TestCallbackMutationCannotChangeCommitEvidence', 'assertions-mapped', 'All-outcome/duplicate response, queued/empty and callback failure assertions now present. Earlier fresh-only baseline is not evidence for these changes; final frozen-source local gates and bounded review closed.')
F('async-command-delivery', 'command-rpc enqueue-operation rpc-delivery-consts', 'Remote unconditioned commands enqueue; local/conditional commands execute directly. Accepted queue is not committed state; workers retain retry identity.', 'command/command_test.go:TestQueuedAcceptanceAndConsume|TestConditionalAndLocalCommandsBypassQueue;delivery/delivery_test.go:TestTasksRouteIdentityAndActualHTTPConsumption|TestSQSReceiptsOnlyAfterDurableSuccess|TestHTTPCancellationAfterCommitDoesNotAcknowledge;integration/framework_flow_test.go:TestGenericAsyncDeliveryAndReplayNative', 'assertions-mapped', 'Actual authenticated delivery/queue receipt and native generic composition assertions present; parent reports generic both-DB flow PASS5.019s. Final frozen-source local gates and bounded review are closed.')
F('gateway-command-authority-upload-tokens', 'command-gateway command-gateway-post', 'Protection/privileges are enforced before command or upload; token/revocation contributions affect configured cookie/header response contract safely.', 'transport/auth_test.go:TestGatewayExactRouteJobAndViewAuthority|TestGatewayBodyResourceAuthorizationPreservesExecutionInput|TestGatewayProtectedContextsAndMissingRegistrations;transport/gateway_test.go:TestGatewayCookieOriginCSRFAndAmbiguity|TestGatewayCookieCommandResponseAndRevocation|TestGatewayCookieFrameworkComposition;transport/upload_test.go:TestGatewayMultipartPreparseBounds|TestGatewayUploadRecoveryAndCancellation|TestGatewayAWSUploadAdapterUsesOfficialStorage|TestGatewayGCPUploadAdapterUsesOfficialStorage', 'assertions-mapped', 'Actual authority/cookie/multipart and official upload adapter fixtures exist. Owner fresh full transport race PASS2.138s and vet PASS; final frozen-source local gates and bounded review closed; no live storage writes.')
F('gateway-fact-proxy-lifecycle', 'fact-gateway fact-gateway-get', 'Proxy preserves allowed upstream headers and cancels producer on disconnect; pre-body and midstream errors are distinguishable.', 'transport/gateway_test.go:TestGatewayFactProxyForwardsRawHeaders|TestGatewayFactProxyRejectsBeforeStreaming|TestGatewayFactProxyCancelsUpstreamOnDisconnect|TestGatewayFactProxyTruncationIsNotSuccess', 'assertions-mapped', 'Gateway owner reports fresh full transport race PASS2.138s and vet PASS. Final frozen-source local gates and bounded review are closed.')
F('cors-origin-policy', 'cors-middleware', 'Only explicitly permitted origins/methods/headers receive CORS grants; denied preflight does not invoke application.', 'transport/cors_test.go:TestCORS', 'deliberate-contract-change', 'Exact configured origins replace ambient network-origin inference; cookie CORS needs gateway integration.')
F('canonical-event-integrity', 'cononical-string hash create-event operation-hash', 'Canonicalization rejects duplicate keys/unsafe values and binds immutable normalized event bytes to digests; typed integers survive roundtrip.', 'eventstore/canonical_test.go:TestCanonical|TestCanonicalTypedInteger;eventstore/metadata_test.go:TestMetadataNormalization|TestInvalidUTF8MetadataCannotBeSilentlyRepaired;integration/store_test.go:TestReducerCannotMutateCommittedBytes', 'deliberate-contract-change', 'Internal canonical SHA256 differs from JS Keccak; external EVM/MPT/RLP retain their own byte contracts. Stable retry fingerprint must separately exclude only volatile JWT fields.')
F('password-hmac-encryption', 'crypt hmac', 'Password derivation uses explicit salt/parameters; authenticated encryption rejects tamper and binds identity; HMAC-backed proofs cannot be forged.', 'dia/auth_test.go:TestPassword|TestPasswordKnownAnswer|TestSeal;dia/system_test.go:TestSystemKeySecretRecoveryAndAuthorization', 'deliberate-contract-change', 'Stdlib primitives replace legacy wrappers. JS formatting/default algorithm compatibility is not asserted.')
F('time-and-duration', 'datetime duration-consts', 'Boundary expiry and deadlines are explicit; exact expiration and cancellation reject further authority/work.', 'dia/auth_test.go:TestJWT;rpc/client_test.go:TestDeadlineAndClose', 'deliberate-contract-change', 'time.Time/Duration replace generic date/time/constants exports; no JS string-format compatibility.')
F('dwolla-customer-owner-document', 'dwolla', 'All retained customer/beneficial-owner status, certification and multipart-document operations preserve request fields and error status without leaking credentials.', 'integrations/dwolla/client_test.go:TestCustomers|TestOwnersAndDocuments|TestErrorStatusAndCredentialRedaction', 'assertions-mapped', '17 operations have typed adapters; callback SDK error-wrapper representation is redesigned. Live provider behavior not verified.')
F('input-normalization', 'email-validator phone-number validation validator uuid-validator', 'Reject malformed or unsafe input before persistence; normalize intended email/phone and typed identifiers without coercing unrelated strings.', 'dia/service_test.go:TestNormalizeContracts;dia/reducers_test.go:TestReducerJSONModels', 'deliberate-contract-change', 'UUID-only shape is retired; externally required binary/decimal formats remain validated. No generic all-validator-library equivalence claim.')
F('errors-http-response-commit', 'error-middleware errors', 'Known invalid/conflict/forbidden/uncertain errors preserve outcome/status; private causes are not leaked and serialization failure cannot commit a success status.', 'transport/http_test.go:TestBoundaryErrors|TestSerializationFailureCannotCommitSuccessStatus;transport/framework_test.go:TestProcedureErrorStatus;view/errors_test.go:TestBackendErrorsNeverBecomeInvalid')
F('store-append-atomicity-idempotency', 'event-store event-store-post event-store-post-transaction mongodb-event-store-create-transaction mongodb-event-store-save-events mongodb-event-store-idempotency-conflict-check mongodb-event-store-reserve-root-counts', 'Atomic multi-event append yields input-aligned outcomes, exact retry success, changed-intent rejection and no partial transaction residue.', 'integration/store_test.go:TestNativeContract;storage/mongo/integration_test.go:TestNativeRollbackAndConstraints|TestNativeConcurrencyAndSnapshot;storage/postgres/integration_test.go:TestNativeConcurrentWriters|TestNativeUniqueConstraintsRollback;eventstore/fingerprint_test.go:TestRequestFingerprintSeparatesTokenLifetimeFromAuthority|TestEventHashStillBindsVolatileClaims;integration/framework_flow_test.go:TestGenericFrameworkNativeFlow', 'assertions-mapped', 'Stable fingerprint excludes only exp/iat/nbf/jti while authority stays sensitive and committed hashes retain original claims. Final frozen-source local gates and bounded review are closed.')
F('store-history-count-stream', 'event-store-aggregate event-store-aggregate-stream event-store-count event-store-get event-store-root-stream event-store-rpc mongodb-event-store-count mongodb-event-store-event-stream mongodb-event-store-find-events mongodb-event-store-root-stream', 'Count, history through event/time selectors and ordered root/aggregate streams use correct source scope, cancellation and single-attempt callback delivery.', 'integration/store_test.go:TestHistoricalReplay|TestStoreStreamingRoutes;storage/mongo/integration_test.go:TestNativeViewDoesNotReplayStreams;rpc/client_test.go:TestBufferedRecordsAfterEOF', 'assertions-mapped', 'Cross-store merged streaming assertions are tracked in multi-store-merge-order; its current/historical HTTP fixtures are implemented, final combined gates closed.')
F('aggregate-query-candidates', 'event-store-query mongodb-event-store-find-one-snapshot mongodb-event-store-find-snapshots', 'Candidate selection includes matching snapshots plus dirty roots, deduplicates/replays once and applies strict dotted/array-aware current-state matches without a hidden limit.', 'storage/mongo/candidates_integration_test.go:TestNativeCandidates|TestNativeCandidateReplay;storage/postgres/candidates_integration_test.go:TestNativeCandidates|TestNativeCandidateReplay;eventstore/canonical_test.go:TestPredicates', 'assertions-mapped', 'Adapter test wrappers delegate to shared querytest assertions; those helper bodies must be read too.')
F('blocks-frontiers-and-large-storage', 'event-store-create-block event-store-create-block-transaction mongodb-event-store-latest-block mongodb-event-store-save-block mongodb-event-store-save-snapshot', 'Sequence frontiers choose membership independent of wall time; snapshots/block advance atomically, fair selection progresses and Mongo chunks preserve oversized bytes/corruption detection.', 'integration/store_test.go:TestNativeContract|TestStoreStreamingRoutes;storage/mongo/body_integration_test.go:TestNative;storage/mongo/frontier_integration_test.go:TestNativeFairFrontier;storage/postgres/frontier_integration_test.go:TestNativeFairFrontier;eventstore/canonical_test.go:TestBlockHashBinding', 'deliberate-contract-change', 'Internal SHA256 JSON block/ES256 export deliberately differs from legacy MPT/RLP wire format; binary primitives are separately mapped.')
F('private-export-authority', '', 'Private exports encrypt bytes and require configured producer ES256 authority; wrong key/signature/metadata and tampered payload reject.', 'audit/export_test.go:TestPrivateExport', 'go-additional-scope', 'Historical block privacy/signing obligation retained through a fresh export envelope.')
F('outbox-publication-delivery', 'event-store-create-publication event-store-deliver-pending mongodb-event-store-publish-pending', 'Append reserves durable notices; delivery acknowledges only durable processing, retries uncertain failures and retains Attempts; complete Publication size rejects before commit.', 'integration/store_test.go:TestNativeContract|TestOversizeEventRollsBackBeforeCommit;runtimepub/aws_test.go:TestAWSConsumeWireValidationAndRetention|TestAWSPublishSizePreflight;runtimepub/providers_test.go:TestGCPRuntimeWire|TestGCPPublishSizePreflight;projection/publication_test.go:TestPublicationVerifiedTriggerAndBestEffortPush|TestPublicationRejectsForgedOrMissingSource;integration/framework_flow_test.go:TestGenericAsyncDeliveryAndReplayNative;dia-go:cmd/document/publication_ownership_native_test.go:TestPublicationOwnershipNative', 'assertions-mapped', 'Trusted publication and generic durable delivery assertions now present. Attempts stays outside publication retry identity; final frozen-source local gates and bounded review closed.')
F('multi-store-merge-order', 'event-stores-rpc', 'Independent event stores are queried/streamed and results merge in deterministic order with historical selectors and proper cancellation/error behavior.', 'transport/store_merge_test.go:TestStoreMergeHistoricalHTTPSelectors|TestStoreMergeCurrentHTTPBudgetAndFailures|TestStoreMergeIncrementalCancellationAndCallbackError', 'assertions-mapped', 'Actual two-server current/historical selectors, full bounded sort, no partial sorted output on failure and incremental cancellation asserted. Explicit sources/comparator/budget replace hidden discovery; final frozen-source local gates and bounded review closed.')
F('server-runtime-composition', 'express-middleware server', 'A real generic service composes command/fact/job/store/publication/projection/view across two HTTP services with trusted scoped identities and actual database adapters.', 'transport/framework_test.go:TestFrameworkMountsScopedAdapters;integration/container_test.go:TestContainerSessionLifecycle;integration/framework_flow_test.go:TestGenericFrameworkNativeFlow|TestGenericAsyncDeliveryAndReplayNative', 'assertions-mapped', 'Concrete serviceapp and cmd/framework exist; parent reports both-DB generic flow PASS5.019s. Process/container and fresh full gates remain separate.')
F('fact-query-serial-stream', 'fact fact-get fact-rpc fact-stream', 'Required context/query validation, serial awaited callbacks, bounded chunk-independent streams and cancellation/failure cleanup remain observable.', 'fact/fact_test.go:TestReadAndChunkIndependentStream|TestStreamAwaitsSerialCallbacks|TestCallbackErrorClosesNetworkStream|TestMalformedOversizedAndFailedStreams|TestConcurrentProducerSerializedAndClosed')
F('mongo-lifecycle-and-namespace', 'mongodb-database mongodb-event-store format-mongodb-schema', 'Native schema/index readiness, namespace isolation, constraints, rollback, retry limits and uncertainty are enforced by the adapter.', 'storage/mongo/integration_test.go:TestNativeEmptyAndRoundtrip|TestNativeRetryAndUncertain|TestNativeRetryBudget|TestNativeProjectionAndNamespace;storage/postgres/integration_test.go:TestNativeNamespaceIsolationAndInitialization|TestNativeUncertainCommit', 'deliberate-contract-change', 'Typed tables/documents replace Mongoose schema compilation; PostgreSQL is additional required Go scope.')
F('gcp-kms-and-aws-crypto', 'gcp-kms', 'Provider signing/verification/public keys and encrypt/decrypt use exact binary/resource parameters; integrity failures reject.', 'cloud/gcp/wire_test.go:TestKMSWireCrypto|TestKMSIntegrityFailures;cloud/aws/wire_test.go:TestKMSNativeCryptoAndES256|TestES256RejectsInvalidProviderSignature;runtimeauth/signing_test.go:TestRuntimeSigningSelection|TestRuntimeSigningRejectsInvalidConfiguration', 'assertions-mapped', 'Contextual signer selection, pinned verification, no startup signing and local bypass assertions now present; provider execution uses injected clients, no live KMS claim.')
F('cloud-service-auth', 'gcp-token', 'Audience-specific GCP identity and AWS SigV4 are constructed explicitly, cancel correctly and never infer application permissions from provider transport auth.', 'cloud/gcp/http_test.go:TestAuthSDKWire;cloud/aws/wire_test.go:TestServiceAuthActualRequest|TestAuthBodyFailureAndCancellation;runtimepub/open_test.go:TestOpenConfigurationFailures|TestOpenAWSOfficialConfigAndClose', 'deliberate-contract-change', 'Cloud service auth is not the application JWT signer. Runtime env credentials are read only at explicit Open.')
F('cloud-pubsub', 'gcp-pubsub', 'Topics/subscriptions, publish, bounded pull and acknowledgments preserve exact provider values and failures.', 'cloud/gcp/wire_test.go:TestPubSubWire;cloud/aws/wire_test.go:TestSNSPublishNativeQuery|TestSQSReceiptDispatchAndError', 'assertions-mapped', 'AWS SNS/SQS has separate queue/policy/raw-delivery requirements.')
F('cloud-task-queue', 'gcp-queue', 'Queue lifecycle and delayed authenticated task body are correct; provider acceptance is not domain commit.', 'cloud/gcp/wire_test.go:TestTasksSchedulerWire;cloud/aws/wire_test.go:TestSQSReceiptDispatchAndError;delivery/delivery_test.go:TestTasksStableAcceptanceAndUncertainty|TestTasksRouteIdentityAndActualHTTPConsumption|TestProviderReplayAndPublicationBindings', 'assertions-mapped', 'Actual typed queue/replay/publication bindings and consumer assertions present; runtimequeue config source exists. Final frozen-source local gates and bounded review are closed.')
F('cloud-scheduler', 'gcp-schedule', 'Scheduled HTTP/provider targets preserve identifiers, authentication, schedule/timezone and failures.', 'cloud/gcp/wire_test.go:TestTasksSchedulerWire;cloud/aws/wire_test.go:TestSchedulerNativeTarget', 'deliberate-contract-change', 'AWS Scheduler target ARN/execution role is not a translated Cloud Tasks cron HTTP call.')
F('cloud-secrets-storage', 'gcp-secret gcp-storage', 'Binary secret/object content and resource names survive upload/download/list pagination without destructive trimming; errors/cancellation propagate.', 'cloud/gcp/http_test.go:TestStorageAndEncryptedSecretsWire;cloud/aws/wire_test.go:TestSecretsPreserveBinary|TestStoragePaginationAndBytes|TestWalkObjectsFailures', 'assertions-mapped', 'Deployment secret refs differ from retained encrypted-GCS-secret wrapper; both owners are explicit.')
F('cloud-logs', 'gcp-log-transports', 'Structured log writes preserve records and surface partial provider rejection without disclosing secret causes.', 'cloud/gcp/wire_test.go:TestLoggingWire;cloud/aws/wire_test.go:TestLogsPartialRejection')
F('csv-value-safety', 'json-to-csv', 'CSV emits paths and typed cells without spreadsheet injection, malformed values or silent writer failures.', 'view/format_test.go:TestCSVValuesAndPaths|TestCSVSpreadsheetStrings|TestCSVValidationBeforeWriting|TestCSVWriterFailure;transport/view_query_test.go:TestViewBootstrapEmptyCSVHooks', 'assertions-mapped', 'Actual HTTP CSV content type/escaping and formatter failure isolation now asserted alongside owner CSV tests. Final frozen-source local gates and bounded review are closed.')
F('job-trusted-claims', 'job job-post job-rpc', 'Job HTTP/RPC forwards authenticated context and claims, validates payload and reports errors rather than fabricating success.', 'job/job_test.go:TestJobHTTP;transport/framework_test.go:TestFrameworkAuthorityAndStreamFailure;transport/command_client_test.go:TestFrameworkJobTrustedClaims', 'assertions-mapped', 'Actual HTTP/RPC rejects forged job Context/Claims by injecting trusted authenticated values; final frozen-source local gates and bounded review closed.')
F('mpt-rlp-external-bytes', 'merkle-tree rlp', 'External MPT root/proof and RLP bytes match independent JS vectors; tampering, missing nodes and malformed encodings fail.', 'cryptography/cryptography_test.go:TestExternalMPTByteVectors|TestMPTTamperingMissingNodeAndRootBinding|TestExternalRLPByteVectors|TestRLPRejectsNoncanonicalTruncatedAndTrailingBytes')
F('rpc-addressing-and-wire', 'network-url operation-url request rpc url-encode-query-data', 'Explicit base/path/query/headers preserve identity; reject unsafe paths/redirects and bounded error bodies redact credentials.', 'rpc/client_test.go:TestWire|TestBounds|TestErrorsAndRedirects|TestInvalidInputs|TestHTTPErrorPreservesMetadataAndRedactsCredentials', 'deliberate-contract-change', 'Explicit URLs replace ambient JS service discovery; cross-service identity propagation separately tested by new contextual clients.')
F('rpc-stream-retry-cancel', 'stream-rpc', 'Streaming calls do not replay side effects or callbacks, do not truncate silently and close/cancel producers on failures.', 'rpc/client_test.go:TestDisconnectDoesNotReplayWrites|TestBufferedRecordsAfterEOF;fact/fact_test.go:TestCallbackErrorClosesNetworkStream|TestStreamingDeadlineReachesCallbackAndCancelsServer')
F('projection-current-mapping-and-mute', 'projection projection-play', 'Play derives from current authoritative aggregate, binds root/revision, commits one batch and pushes only applied unmuted trusted-event changes.', 'projection/projection_test.go:TestReferenceCurrentAggregateAndMutedPlay|TestReferenceNoAppliedViewDoesNotPush|TestPushFailuresAreAfterCommitAndContinue|TestCommitFailureNeverPushes|TestSourceIdentityAndRevisionValidation;projection/native_test.go:TestNativeProjectionOutOfOrderAndTombstones;projection/publication_test.go:TestPublicationVerifiedTriggerAndBestEffortPush|TestPublicationRejectsForgedOrMissingSource;integration/framework_flow_test.go:TestGenericAsyncDeliveryAndReplayNative', 'assertions-mapped', 'Trusted publication maps into generic engine and checks source integrity; best-effort push failure does not rewrite durable projection outcome. Final frozen-source local gates and bounded review are closed.')
F('projection-recursion-replay', 'projection-replay projection-rpc', 'Recursive dependency preparation detects cycles/budgets before writes; replay buffers bounded roots outside producer effects, retains partial cursor and scopes requests. One explicit logical RunID is retained across retries/pages and echoed in acknowledgments; new runs use new IDs.', 'projection/projection_test.go:TestDependenciesAreSerialVersionedAndPreparedBeforeCommit|TestDependencyFailureAndBudgetsHaveNoWrites|TestOptionalMissingAndHandlerIsolation;projection/replay_test.go:TestReplayBoundedManualSchedulingAndResume|TestReplayConfirmedProgressOnProducerFailure|TestReplayScopeIsolation;transport/projection_routes_test.go:TestProjectionRPCRequiredOutcomes;delivery/delivery_test.go:TestReplayRunIdentityAndTriggerRejection|TestProviderReplayAndPublicationBindings;integration/framework_flow_test.go:TestGenericAsyncDeliveryAndReplayNative;cli/replay_test.go:TestReplayRequiresLogicalRunAndValidatedAcknowledgment|TestRejectStaticReplayConfiguration', 'assertions-mapped', 'Explicit request RunID replaces static process and per-message IDs. CLI uses owning ProjectionClient response validation and rejects obsolete config; final frozen-source local gates and bounded review closed.')
F('redis-hash-ttl', 'redis', 'Remote binary hashes merge/read with AUTH/SELECT and expiration; explicit memory backend isolates copies and expiry, never silently replaces failed remote Redis.', 'integrations/redis/client_test.go:TestRemoteWireAndErrors|TestMemoryExpiryAndIsolation|TestMalformedRESP;integrations/redis/integration_test.go:TestNativeRedisHashAndExpiry')
F('random-identifiers', 'nonce random-char random-int random-int-of-length random-string-of-length uuid', 'Fresh credentials/nonces come from cryptographic randomness, are validated at boundaries and do not let malformed retry keys probe secrets.', 'dia/service_test.go:TestSessionLifecycleAndCreationCredential|TestChallengeSameKeyCannotProbeMultipleCodes;wallet/wallet_test.go:TestHTTPStrictIntegerAndKeyBoundary', 'deliberate-contract-change', 'No retained general-purpose Math.random/UUID/string-alphabet API. Domain-specific crypto/rand contracts replace JS helpers; unrelated legacy formatting is not implied.')
F('view-mutations-and-atomicity', 'compose-update view-store view-store-delete view-store-put view-store-mutations mongodb-view-store', 'Typed set/inc/push/pull/array-filter operations preserve owner scope, conditional atomicity and version fences; deletion/tombstones prevent stale resurrection.', 'view/mutation_test.go:TestComposeMutations|TestFilteredNestedComposition|TestPullObjectsAndCanonicalSet|TestMutationRejectsInvalidAndDoesNotCoerce|TestMutationFailureDoesNotPartiallyModifyInput;view/native_test.go:TestNativeViewAtomicFailureAndConditions|TestNativeViewConcurrentVersionsAndPatch|TestNativeViewTombstoneBeforeCreate;view/query_native_test.go:TestNativeViewMutateWhereAtomicSelection|TestNativeViewNamedIsolationSameRootIDVersion', 'assertions-mapped', 'Actual native query-addressed atomic mutations and named-store isolation assertions now present; final native results are recorded in verification_stage.')
F('view-query-pagination-groups', 'view-store-get view-store-id-stream view-store-rpc', 'Typed number/date queries, ranges, text and custom sort preserve auth group union and pagination; named stores isolate identical IDs; unsafe wire markers reject.', 'view/query_test.go:TestScopedListPagination|TestListFailsClosed;view/native_test.go:TestNativeViewListingPagesAndCancellation|TestNativeViewRepeatablePagination;view/query_extended_test.go:TestTypedRangesDateTextSortPagination|TestTypedSortArraysMissingAndTies|TestExtendedQueryInvalidBeforeBackend|TestOwnerAnyIsUnionUnderCommonAuthority;transport/view_query_test.go:TestViewQueryRoundtripTextPaginationGroups', 'assertions-mapped', 'Typed range/text/sort, group union under common authority, next-link roundtrip and caller scope-widening negatives now asserted. Final frozen-source local gates and bounded review are closed.')
F('view-gateway-authority-channel', 'view-gateway view-gateway-get view-gateway-channel channel-name', 'Named scoped reads/list/batch and channel lookup cannot select another tenant/source/group; bootstrap and updates links require explicit authority.', 'transport/view_gateway_test.go:TestViewGatewayScopesAndStrictInput|TestViewGatewayBatchRollback|TestViewGatewayCompositeAndChannel|TestViewGatewayRPC;transport/view_query_test.go:TestViewBootstrapEmptyCSVHooks|TestViewNamedIsolationSameRootIDVersion|TestViewQueryRoundtripTextPaginationGroups', 'assertions-mapped', 'Bootstrap scoped lookup, empty hook, updates link and named-store isolation asserted. Channel lookup/metadata is not a deployed SSE/GRIP service; final frozen-source local gates and bounded review closed.')
F('view-composite-isolation-stream', 'view-composite view-composite-get view-composite-rpc', 'Components bind trusted scope and cancellation, reject foreign/deleted rows, forward typed queries and abort failed streams.', 'view/composite_test.go:TestCompositeTypedQueryResponseAndOwnerFiltering|TestCompositeScopeCannotBeOverridden|TestCompositeRejectsReaderScopeViolations|TestCompositeCancellationAndReaderLifetime;transport/view_gateway_test.go:TestViewGatewayStreamFailureAndCancellation|TestViewGatewayStreamWireAbort|TestViewGatewayStreamUsesSharedContract', 'deliberate-contract-change', 'Reads are not a cross-component atomic snapshot; no fabricated live subscription claim.')
F('evm-v6-forwarder', 'erc2771 contract-abis-trusted-forwarder contract-addresses-trusted-forwarder', 'Pinned V6 address/ABI/domain and typed integer signing boundaries preserve exact chain-specific signing authority.', 'evm/forwarder_test.go:Test;evm/event_numbers_test.go:Test', 'assertions-mapped', 'JB/shop and Relayr payments excluded; V6 signing retained; no deployed-bytecode/live-chain verification.')
F('evm-providers-and-custody', 'providers', 'Explicit RPC chain identity and durable signed transaction journal survive uncertain broadcasts, retries/restart/reorg/receipt observation without exposing private keys.', 'evm/evm_test.go:TestCustodyAuthenticatedBindings|TestDurableBeforeSubmitAndUncertainRetry|TestPersistenceFailureCannotSubmit|TestConcurrentNoncePolicyAndRestart|TestBoundedReceiptReconciliation|TestActualEthclientRPC;wallet/native_test.go:TestNativeCustodialJourney|TestNativeJournalAtomicity', 'go-additional-scope', 'Direct custodial wallet is additional user scope beyond provider wrapper; arbitrary payments are not included.')
F('comms-provider-and-durability', 'twilio-sms', 'Explicit provider inputs and authority, encrypted durable intent before send, failed/uncertain delivery retry without duplicate intent or secret leakage.', 'dia/adapters_test.go:TestHTTPAdapters|TestAdapterFailures|TestAdapterRedirectDoesNotReplay|TestSMTPRejectsUnsafeInputAndPlaintext;dia/system_test.go:TestSystemCommsCommandsPersistEncryptedIntentBeforeDelivery;dia/operation_test.go:TestCommsUncertainUnreadableReceiptDoesNotSend')
F('retired-language-and-display-tooling', 'hash-string operation-short-name robo-say trim-string logger service-name-consts', 'Typed identifiers, explicit service config and structured logs replace JS decorative output, lossy trimming and hash-based operation naming.', 'cli/cli_test.go:TestStrictConfiguration|TestSubprocessFailureAndOfflineEnvironment', 'deliberate-contract-change', 'No claim that unrelated display/logger wrapper output or string trimming is preserved; reference test cases are retained for review.')

# Dia is explicitly additional retained application scope, not hidden in154 packages.
F('dia-session-scene-lifecycle', '', 'Session create/retry/terminate/scene upgrade and protected view enforce persisted authority and exact recovery without anonymous privilege escalation.', 'dia/service_test.go:TestSessionLifecycleAndCreationCredential|TestSceneUpgradeLostResponseRecoveryIsBounded|TestChangeSceneLostResponseRecoversOnlyCommittedTarget;integration/session_test.go:TestDiaSessionFlow', 'assertions-mapped')
F('dia-account-challenge-identity', '', 'Account/challenge issuance and answer bind intent, attempts, proof and exact receipt; concurrent retries recover only the committed request.', 'dia/service_test.go:TestAccountChallengeAtomicUpgradeAndReplay|TestChallengeAttemptLimitAndIntentBinding|TestConcurrentSameAnswerKeyRecoversExactCommit|TestConsumedChallengeRecoveryCannotBecomeGuessOracle;dia/operation_test.go:TestAppendErrorReconcilesOnlyExactVisibleReceipt', 'assertions-mapped')
F('dia-role-group-authority', '', 'Role/group grants and removals preserve subject boundaries, role-definition freshness and retry/regrant ordering; streams do not leak groups.', 'dia/identity_race_test.go:TestDynamicRoleGrantExpansionInterleavings|TestBuiltinRoleGrantDoesNotTouchDefinition;dia/service_test.go:TestSceneGroupAndRoleAuthority|TestGroupCreationRetryAndRemovalKeepRolesAligned', 'assertions-mapped')
F('dia-system-node-key-connection', '', 'Atomic node registration, secret/key recovery, rotation, connection credentials and revocation bind caller/subject and recover exact results.', 'dia/system_test.go:TestSystemNodeAtomicRegistrationAndReplay|TestSystemKeySecretRecoveryAndAuthorization|TestSystemConnectionUpdatesAndRevocation|TestSystemConnectionBindingAndKeyRotation|TestSystemNodeLostResponseRecoveryIsExact', 'assertions-mapped')


def digest(data):
    return hashlib.sha256(data.encode()).hexdigest()


def go_tests(selector):
    file, expression = selector.rsplit(':', 1)
    path = source_path(file)
    if not path.is_file():
        return [], dict(selector=selector, reason='file not present at snapshot')
    text = path.read_text()
    # Ignore declarations/assertions inside generated Go source literals and comments.
    tokens = r'''(?s)//[^\n]*|/\*.*?\*/|`[^`]*`|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*' '''.rstrip()
    masked = re.sub(tokens, lambda m: re.sub(r'[^\n]', ' ', m[0]), text)
    funcs = list(re.finditer(r'(?m)^func\s+(\w+)\s*(?:\[[^\n]*\])?\(', masked))
    selected = []
    for index, match in enumerate(funcs):
        if not re.search(expression, match[1]):
            continue
        end = funcs[index+1].start() if index+1 < len(funcs) else len(text)
        block = text[match.start():end]
        line = text.count('\n', 0, match.start())+1
        lines = block.splitlines()
        assertions = []
        masked_lines = masked[match.start():end].splitlines()
        for offset, value in enumerate(lines):
            if re.search(r'\bt\.(?:Fatalf?|Errorf?)\(', masked_lines[offset]):
                assertions.append(dict(line=line+offset, code=value.strip(),
                                       predicate_context='\n'.join(lines[max(0,offset-3):offset+2]).strip()))
        selected.append(dict(file=file, test=match[1], line=line, body_sha256=digest(block),
                             assertions=assertions, execution='current source inspected; split execution gates tracked separately'))
    return selected, None if selected else dict(selector=selector, reason='test/function not present at snapshot')


def build():
    inventory = json.loads((ROOT/'docs/capabilities.json').read_text())
    saved = json.loads((ROOT/'docs/behavior-coverage.json').read_text())
    if not DIA_SOURCE.is_dir():
        raise SystemExit('Dia source unavailable: set DIA_SOURCE for two-repository ledger verification (not required to build Toolbox).')
    packages = {p['name']:p for p in inventory['packages']}
    if len(packages) != 154 or set(packages) != set(saved['package_family_assignment']):
        raise SystemExit('Retained package identity set changed; explicit inventory review required.')
    for package in packages.values():
        for kind in ('source_files', 'test_files'):
            for file in package['implementation'][kind]:
                if not source_path(file).is_file():
                    raise SystemExit('Missing current capability owner: ' + file)
    previous = {f['id']:f for f in saved['families']}
    if {f['id'] for f in FAMILIES} != set(previous):
        raise SystemExit('Behavior family identity set changed; explicit inventory review required.')
    # Frozen historical inputs: refreshing Go anchors must never rediscover a
    # smaller JS catalog because an unrelated checkout is absent.
    js_catalog = saved['reference_test_catalog']
    integrity = inventory['coverage_reference_integrity']
    def preserved(key, value):
        actual = hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
        if actual != integrity[key]:
            raise SystemExit('Frozen reference integrity changed: ' + key)
    preserved('reference_test_catalog_sha256', js_catalog)
    preserved('family_reference_ids_sha256', {f['id']: f['js_case_ids'] for f in saved['families']})
    preserved('package_family_assignment_sha256', saved['package_family_assignment'])
    historical_set = {f['id']: f.get('historical_go_assertions', [
        {k: a[k] for k in ('file', 'test', 'body_sha256')} for a in f['go_assertions']]) for f in saved['families']}
    preserved('historical_go_assertions_sha256', historical_set)
    assigned, families = {}, []
    for specification in FAMILIES:
        family = dict(specification)
        old = previous[family['id']]
        for name in family['packages']:
            assert name in packages and name not in assigned, name
            assigned[name] = family['id']
        family['reference_test_files'] = old['reference_test_files']
        family['go_assertions'], family['unresolved_go_selectors'] = [], []
        for selector in family.pop('go_selectors'):
            file, expression = selector.rsplit(':', 1)
            for old_selector, (new_name, reason) in TEST_REPLACEMENTS.items():
                old_file, old_name = old_selector.rsplit(':', 1)
                if file == old_file:
                    expression = expression.replace(old_name, new_name)
            matches = []
            for mapped in current_paths(file):
                tests, _ = go_tests(mapped + ':' + expression)
                matches.extend(tests)
            family['go_assertions'].extend(matches)
            if not matches:
                family['unresolved_go_selectors'].append(dict(selector=selector, reason='No current owning source matched after relocation'))
        historical = old.get('historical_go_assertions', [
            {k: t[k] for k in ('file', 'test', 'body_sha256')} for t in old['go_assertions']])
        family['historical_go_assertions'] = historical
        current_names = {t['test'] for t in family['go_assertions']}
        family['selector_replacements'] = []
        for anchor in historical:
            key = anchor['file'] + ':' + anchor['test']
            replacement = TEST_REPLACEMENTS.get(key)
            expected = replacement[0] if replacement else anchor['test']
            if replacement:
                family['selector_replacements'].append(dict(previous=key, current=anchor['file'] + ':' + expected, reason=replacement[1]))
            if expected not in current_names:
                family['unresolved_go_selectors'].append(dict(
                    selector=anchor['file'] + ':' + anchor['test'],
                    reason='Historical assertion requires explicit current replacement; never silently dropped'))
        family['js_case_ids'] = old['js_case_ids']
        family['assertion_count'] = sum(len(t['assertions']) for t in family['go_assertions'])
        family['coverage_boundary'] = 'Current repository-qualified source anchors are inspected, not execution evidence. Historical notes and assertion hashes describe the pre-split snapshot. All original JS cases remain indexed; family associations do not prove one-to-one parity.'
        if not family['assertion_count'] and family['disposition'] not in ('missing-assertion','deliberate-contract-change'):
            family['disposition'] = 'missing-assertion'
        families.append(family)
    assert assigned == saved['package_family_assignment'], 'Retained package assignments changed'
    return dict(schema_version=2, status=inventory['verification_stage']['overall'],
                reference_commit=inventory['reference_commit'],
                historical_snapshot=inventory['historical_snapshot'],
                source_repositories=inventory['source_repositories'],
                baseline_evidence=saved['baseline_evidence'],
                current_checkpoint=inventory['verification_stage'].get('current_checkpoint', {}),
                review=inventory['independent_review'],
                policy='All retained identities/reference cases are preserved. Repository-qualified assertion anchors are source evidence only; historical passes do not verify this split.',
                required_completion=inventory['verification_stage']['remaining'],
                package_family_assignment=assigned, families=families, reference_test_catalog=js_catalog,
                additional_dia_reference_tests=saved['additional_dia_reference_tests'],
                excluded_dia_reference_tests=saved['excluded_dia_reference_tests'],
                counts=dict(packages=len(assigned), families=len(families), reference_files=len(js_catalog),
                            literal_js_test_call_sites=sum(len(v['cases']) for v in js_catalog.values())))


def markdown(ledger):
    lines=['# Retained behavior coverage','', '**Current verification status: '+ledger['status']+'.** '+ledger['required_completion'], '',
           'This ledger maps observable JS contracts to actual Go test assertions. It is not a package-count or passing-suite equivalence claim. Historical gate results and reviews apply to the pre-split snapshot only. Current split verification is tracked separately in capabilities.json.', '',
           'The [machine matrix](behavior-coverage.json) includes every retained package assignment, all indexed JS test call-sites/assertion lines, selected Go test names/assertion predicates and explicit unresolved selectors. Run `DIA_SOURCE=/path/to/dia-go python3 docs/behavior-coverage.py --check` to verify both source trees (default: sibling `../dia-go`). This documentation check is not a Go build dependency. `--refresh` updates current Go anchors while preserving the frozen JS catalog and historical assertion identities; it does not run tests or mark gaps resolved. Unresolved relocations fail the check. Dia links use the recorded commit when pinned; review branches are used until the paired change is approved.', '',
           'The pinned Toolbox reference is `'+ledger['reference_commit']+'`. Dia test references use the preserved reference workspace Dia source with file hashes. JB/shop and Relayr payment test files are listed as exclusions; direct custodial-wallet tests remain included. Literal JS call-sites are not expanded parameterized case counts.', '',
           'Each row has a specific contract and Go predicate excerpts in JSON. `assertions-mapped` is source evidence only. `deliberate-contract-change` names a fresh internal contract, not permission to drop a retained behavior. `remediation-active` and `missing-assertion` remain incomplete. The current checkpoint above governs execution status. Row notes preserve contract boundaries and historical scoped reports; they do not override the current gate state.', '',
           '| Behavior family | Observable contract | Go assertion anchors | Disposition / contract boundaries and checkpoint notes |','| --- | --- | --- | --- |']
    for f in ledger['families']:
        anchors=', '.join(f'[{t["test"]}]({source_link(t["file"], ledger["source_repositories"], t["line"])})' for t in f['go_assertions']) or 'No matching Go assertion yet'
        gap=f['disposition']+(': '+f['remaining'] if f['remaining'] else '')
        for change in f.get('selector_replacements', []):
            gap += ' Explicit replacement: ' + change['previous'] + ' -> ' + change['current'] + '. ' + change['reason']
        if f['unresolved_go_selectors']:gap+=' Unresolved selectors: '+', '.join(p['selector'] for p in f['unresolved_go_selectors'])
        lines.append('| '+f['id']+' | '+f['contract'].replace('|','\\|')+' | '+anchors+' | '+gap.replace('|','\\|')+' |')
    lines+=['','## Checkability and limits','',
            f'The ledger assigns all{ledger["counts"]["packages"]} retained package names and inventories{ledger["counts"]["families"]} behavior families. These are bookkeeping counts, not test coverage percentages. A test file/function without an actual assertion is not promoted to semantic evidence. Shared native helper calls require inspection of the helper predicates too.', '',
            'The machine reference catalog retains literal JS tests not individually paired to a unique Go assertion; no one-to-one coverage percentage is claimed. No exhaustive historical branch/default parity is inferred from the family association. All pre-split gates and bounded reviews have their recorded historical scopes. Current split gates require fresh evidence tied to both repository revisions.', '',
            'No cloud deployment, live payment/chain operation or production provider compatibility is established. Dia owns examples/document and cmd/document; Toolbox owns generic framework components. Cross-repository links do not create a framework dependency on the application.']
    return '\n'.join(lines)+'\n'


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--refresh',action='store_true')
    parser.add_argument('--check',action='store_true')
    args=parser.parse_args()
    ledger=build()
    target=ROOT/'docs/behavior-coverage.json'
    md=ROOT/'docs/behavior-coverage.md'
    if args.refresh:
        target.write_text(json.dumps(ledger,indent=2)+'\n')
        md.write_text(markdown(ledger))
    else:
        saved=json.loads(target.read_text())
        if ledger!=saved:raise SystemExit('Stale coverage anchors: inspect changed tests/decisions, then --refresh. No coverage status was automatically promoted.')
        if md.read_text()!=markdown(saved):raise SystemExit('Behavior Markdown and machine ledger differ.')
    unresolved = sum(len(f['unresolved_go_selectors']) for f in ledger['families'])
    print(json.dumps(ledger['counts']))
    if unresolved:
        raise SystemExit(f'{unresolved} unresolved owning-source/test mappings; inspect ledger before claiming completeness.')
    print('Source assertions indexed; current verification status: '+ledger['status']+'. No one-to-one JS parity or live evidence claimed.')

if __name__=='__main__': main()
