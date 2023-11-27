package pot

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// Unique is an interface that is used to identify a model.
type Unique interface {
	Key() string
}

// APIClient is a simple interface that calls the Pot API server.
// It is intended to be used in cases where the Pot Server runs
// separately and a go application wants to connect to it.
//
// APIClient is typed for a single model type, which is used to
// decode the response from the Pot API server.
type APIClient[T Unique] struct {
	// BaseURL is the base URL of the Pot API server.
	BaseURL string

	// client is the HTTP client used to make requests to the Pot API server.
	client *http.Client
}

// NewAPIClient creates a new APIClient.
func NewAPIClient[T Unique](baseURL string) *APIClient[T] {
	if baseURL[len(baseURL)-1] != '/' {
		baseURL += "/"
	}

	return &APIClient[T]{
		BaseURL: baseURL,
		client:  http.DefaultClient,
	}
}

// Get calls the GET method on the Pot API server.
func (c *APIClient[T]) Get(urlPath string) (map[string]T, error) {
	content := map[string]T{}

	resp, err := c.client.Get(c.BaseURL + urlPath)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(&content); err != nil {
		return nil, err
	}

	return content, nil
}

// Create calls the POST method on the Pot API server.
func (c *APIClient[T]) Create(urlPath string, obj []T, co ...CallOpt) error {
	opts := &CallOpts{}
	for _, opt := range co {
		opt(opts)
	}

	content := map[string]T{}

	for _, o := range obj {
		content[o.Key()] = o
	}

	b, err := json.Marshal(content)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+urlPath, bytes.NewReader(b))
	if err != nil {
		return err
	}
	q := req.URL.Query()
	q.Set("batch", "true")
	if opts.norewrite {
		q.Set("norewrite", "true")
	}
	req.URL.RawQuery = q.Encode()

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusLocked {
		return ErrNoRewriteViolated
	}

	return nil
}

// Remove calls the DELETE method on the Pot API server.
func (c *APIClient[T]) Remove(urlPath string, keys ...string) error {
	req, err := http.NewRequest(http.MethodDelete, c.BaseURL+urlPath, nil)
	if err != nil {
		return err
	}
	q := req.URL.Query()
	for _, key := range keys {
		q.Add("key", key)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}
