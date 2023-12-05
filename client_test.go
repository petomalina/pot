package pot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

func newTestAPIClient() *Client[testStruct] {
	return NewClient[testStruct]("http://localhost:8080")
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

func TestListPaths(t *testing.T) {
	testPath := "test/path"
	cleanup(t, testPath)

	client := newTestAPIClient()

	// first make sure there is nothing stored on the path
	res, err := client.ListPaths(testPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Paths) != 0 {
		t.Fatalf("expected no paths, got %v", res)
	}

	// store an object on the path
	_, err = client.Create(testPath, []testStruct{{ID: "test"}}, WithNoRewrite(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	// get the object from the path
	res, err = client.ListPaths(testPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Paths) != 1 {
		t.Fatalf("expected 1 path, got %v", res)
	}

	if res.Paths[0] != testPath {
		t.Fatalf("expected test, got %v", res.Paths)
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
	_, err = client.Create(testPath, []testStruct{obj})
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
			client := newTestAPIClient()
			defer wg.Done()

			obj := testStruct{
				ID:  "test",
				Age: i,
			}

			_, err := client.Create(testPath, []testStruct{obj}, WithNoRewrite(time.Minute))
			if err != nil {
				errs <- err
				return
			}

			slog.Info("election winner", slog.Int("p", i))

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

func TestReElection(t *testing.T) {
	testPath := "test/path"
	cleanup(t, testPath)

	wg := sync.WaitGroup{}
	wg.Add(2)

	errs := make(chan error, 100)

	// testFn is a function simulating single client that tries to get the lock on the path
	// The test runs following steps:
	// - both clients try to get the lock on the path
	// - one of them gets the lock and updates the object, becoming primary
	// 	 the other one fails to get the lock and receives ErrNoRewriteViolated error
	// - primary client updates the object again, ensuring it can update the object
	//   secondary client tries to update the object, but fails with ErrNoRewriteViolated
	// - primary client waits for the lock to expire
	// - secondary client tries to get the lock and succeeds
	testFn := func(id string) {
		defer wg.Done()
		client := newTestAPIClient()

		// primary flags the client as the one holding the lock
		primary := true

		// try to get the lock on both, one of these processes must get the violation error
		_, err := client.Create(testPath, []testStruct{{ID: "leader", Age: 1}}, WithNoRewrite(time.Second*5))
		if err != nil {
			if errors.Is(err, ErrNoRewriteViolated) {
				primary = false
			} else {
				errs <- fmt.Errorf("secondary must fail on rewrite violation, but failed on: %w", err)
			}
		}
		slog.Info("election result", slog.String("p", id), slog.Bool("primary", primary))

		// try updating once more to make sure primary can update while secondary can't
		_, err = client.Create(testPath, []testStruct{{ID: "leader", Age: 2}}, WithNoRewrite(time.Second*5))
		if err != nil {
			if errors.Is(err, ErrNoRewriteViolated) && primary {
				errs <- fmt.Errorf("primary failed to update the object through ownership: %w", err)
			}

			if !errors.Is(err, ErrNoRewriteViolated) && !primary {
				errs <- fmt.Errorf("secondary must fail on rewrite violation, but failed on: %w", err)
			}
		}

		time.Sleep(time.Second * 5)
		if !primary {
			_, err = client.Create(testPath, []testStruct{{ID: "leader", Age: 1}}, WithNoRewrite(time.Second*5))
			if err != nil {
				errs <- fmt.Errorf("secondary failed to get the lock after desired time")
			}
		}
	}

	go testFn("1")
	go testFn("2")

	wg.Wait()
	close(errs)

	var errslice []error
	for err := range errs {
		errslice = append(errslice, err)
	}

	if len(errslice) > 0 {
		t.Fatal(errslice)
	}
}
func TestNoRewriteDuration(t *testing.T) {
	const testPath = "test/path"
	cleanup(t, testPath)

	client := newTestAPIClient()

	_, err := client.Create(testPath, []testStruct{{ID: "test"}}, WithNoRewrite(time.Second*10))
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Create(testPath, []testStruct{{ID: "test"}}, WithNoRewrite(time.Second*10))
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Create(testPath, []testStruct{{ID: "test"}}, WithNoRewrite(time.Second*10))
	if err != nil {
		t.Fatal(err)
	}
}
