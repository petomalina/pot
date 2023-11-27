package pot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

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

func cleanup(t *testing.T, testPath string) {
	t.Helper()

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
}

func TestFlow(t *testing.T) {
	testPath := "test/path"
	cleanup(t, testPath)

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
	err = client.Create(testPath, []testStruct{obj})
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

func TestElection(t *testing.T) {
	testPath := "test/path"
	cleanup(t, testPath)

	client := newTestAPIClient()

	// Run 5 different "clients" that will try to create the same object.
	// Only one of them should succeed, while all others should receive
	// the http.StatusLocked error and the content of the first client.

	errs := make(chan error, 100)
	wg := sync.WaitGroup{}

	mut := sync.Mutex{}
	ageIndex := 0

	for i := 0; i < 5; i++ {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			obj := testStruct{
				ID:  "test",
				Age: i,
			}

			err := client.Create(testPath, []testStruct{obj}, WithNoRewrite(0))
			if err != nil {
				errs <- err
				return
			}

			mut.Lock()
			ageIndex = i
			mut.Unlock()
		}(i)
	}

	wg.Wait()
	close(errs)

	i := 0
	for err := range errs {
		if !errors.Is(err, ErrNoRewriteViolated) {
			t.Fatalf("expected no rewrite violation error, got %v", err)
		}

		i++
	}

	if i != 4 {
		t.Fatalf("expected 4 errors, got %v", i)
	}

	// get the object from the path
	content, err := client.Get(testPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(content) != 1 {
		t.Fatalf("expected 1 object, got %v", content)
	}

	if content["test"].Age != ageIndex {
		t.Fatalf("expected %v, got %v", ageIndex, content["test"].Age)
	}
}

func TestNoRewriteDuration(t *testing.T) {
	const testPath = "test/path"
	cleanup(t, testPath)

	client := newTestAPIClient()

	err := client.Create(testPath, []testStruct{{ID: "test"}})
	if err != nil {
		t.Fatal(err)
	}

	err = client.Create(testPath, []testStruct{{ID: "test2"}}, WithNoRewrite(time.Second*2))
	if err != nil && !errors.Is(err, ErrNoRewriteViolated) {
		t.Fatalf("expected no rewrite violation error, got %v", err)
	}

	time.Sleep(time.Second * 2)

	err = client.Create(testPath, []testStruct{{ID: "test3"}})
	if err != nil {
		t.Fatal(err)
	}
}
