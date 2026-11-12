package aws

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// WalkObjects visits every page, preserving provider order, without buffering a
// whole bucket. Returning an error stops listing. Input is never mutated.
func (a Storage) WalkObjects(ctx context.Context, in *s3.ListObjectsV2Input, visit func(types.Object) error) error {
	if a.Client == nil || in == nil || visit == nil {
		return errors.New("aws s3: client, input and visitor required")
	}
	input := *in
	pages := s3.NewListObjectsV2Paginator(a.Client, &input, func(o *s3.ListObjectsV2PaginatorOptions) { o.StopOnDuplicateToken = true })
	for pages.HasMorePages() {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, object := range page.Contents {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(object); err != nil {
				return err
			}
		}
		if page.IsTruncated != nil && *page.IsTruncated && (page.NextContinuationToken == nil || *page.NextContinuationToken == "" || (input.ContinuationToken != nil && *input.ContinuationToken == *page.NextContinuationToken)) {
			return errors.New("aws s3: truncated listing failed to advance continuation token")
		}
		input.ContinuationToken = page.NextContinuationToken
	}
	return nil
}
