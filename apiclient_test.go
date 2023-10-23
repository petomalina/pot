package pot

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

type testStruct struct {
	ID         string   `json:"id"`
	Age        int      `json:"age"`
	Path       []string `json:"path"`
	NiceThings []struct {
		Name string `json:"name"`
	}
}

func (t testStruct) Key() string {
	return t.ID
}

func newTestAPIClient() *APIClient[testStruct] {
	return NewAPIClient[testStruct]("http://localhost:8080")
}

func TestFlow(t *testing.T) {
	testPath := "test/path"
	gcs, err := storage.NewClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// cleanup before the test
	bucket := gcs.Bucket("petomalina-pot-tests")
	objs := bucket.Objects(context.Background(), &storage.Query{Prefix: testPath})
	for {
		obj, err := objs.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			t.Fatal(err)
		}

		if err := bucket.Object(obj.Name).Delete(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	// run the test
	client := newTestAPIClient()

	// first make sure there is nothing stored on the path
	content, err := client.Get(testPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(content) != 0 {
		t.Fatalf("expected no content, got %v", content)
	}

	// store an object on the path
	obj := testStruct{
		ID:  "test",
		Age: 10,
		Path: []string{
			"test", "path", "to", "test",
		},
		NiceThings: []struct {
			Name string `json:"name"`
		}{
			{Name: "test"},
			{Name: "test2"},
		},
	}
	err = client.Create(testPath, obj)
	if err != nil {
		t.Fatal(err)
	}

	// get the object from the path
	content, err = client.Get(testPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(content) != 1 {
		t.Fatalf("expected 1 object, got %v", content)
	}

	objMarshal, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}

	contentMarshal, err := json.Marshal(content["test"])
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(objMarshal, contentMarshal) {
		t.Fatalf("expected %v, got %v", obj, content)
	}

	// remove the object from the path
	err = client.Remove(testPath, obj.ID)
	if err != nil {
		t.Fatal(err)
	}

	// get the object from the path
	content, err = client.Get(testPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(content) != 0 {
		t.Fatalf("expected no content, got %v", content)
	}
}
