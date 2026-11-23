package backend

import (
	"errors"
	"fmt"
	"testing"

	"cloud.google.com/go/storage"
	azure "github.com/Azure/azure-sdk-for-go/storage"
)

// A delete of an object that is already gone succeeds, as it does on S3 and
// MinIO, so `flynn-blobstore cleanup` can be re-run.
func TestGCSDeleteIgnoresMissingObject(t *testing.T) {
	if err := gcsDeleteErr(storage.ErrObjectNotExist); err != nil {
		t.Fatalf("missing object: %v", err)
	}
	if err := gcsDeleteErr(fmt.Errorf("wrapped: %w", storage.ErrObjectNotExist)); err != nil {
		t.Fatalf("wrapped missing object: %v", err)
	}
	other := errors.New("permission denied")
	if err := gcsDeleteErr(other); err != other {
		t.Fatalf("expected %v, got %v", other, err)
	}
	if err := gcsDeleteErr(nil); err != nil {
		t.Fatal(err)
	}
}

func TestAzureDeleteIgnoresMissingBlob(t *testing.T) {
	if err := azureDeleteErr(azure.AzureStorageServiceError{StatusCode: 404, Code: "BlobNotFound"}); err != nil {
		t.Fatalf("missing blob: %v", err)
	}
	denied := azure.AzureStorageServiceError{StatusCode: 403, Code: "AuthorizationFailure"}
	if err := azureDeleteErr(denied); err == nil {
		t.Fatal("expected a 403 to be returned")
	}
	other := errors.New("connection reset")
	if err := azureDeleteErr(other); err != other {
		t.Fatalf("expected %v, got %v", other, err)
	}
	if err := azureDeleteErr(nil); err != nil {
		t.Fatal(err)
	}
}
