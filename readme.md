# 🍲 Pot

Pot is an incredibly simple and lightweight implementation of a database based on the Cloud Storage.
It let's you store, read, and delete any kind of data in your bucket.

## Running Pot

To run Pot, you need to have a Google Cloud Storage bucket. You can create one [here](https://console.cloud.google.com/storage/create-bucket). Pot then uses the local credentials to access the bucket. You can find more information about the credentials [here](https://cloud.google.com/docs/authentication/getting-started).

Pot requires only single flag to run:

```bash
$ pot -bucket <bucket-name>
```

Pot runs by default on port `8080` and doesn't respect any other opinions on port selection. It is intended to be ran in a serverless environment or an environment that supports port forwarding.

## Using Pot

Pot is a simple HTTP server that exposes three endpoints:
- `GET /<path>`: Returns the data stored at the given path.
- `POST /<path>`: Creates a new document at the given path. The body of the request is used as the data. Either `id` or `name` are used as they key of the document (`id` takes precedence).
- `DELETE /<path>?key=<key>`: Deletes the document at the given path with the given key.

Pot doesn't support any kind of filtering or querying a single document. Pot always returns all data on the gvien path. If you wish to store documents separately, you can use the `id` or `name` as the path.

## Examples

### Storing a document

This example stores a document with key `John Doe` at the path `users`:

```bash
$ curl -X POST -d '{"name": "John Doe", "age": 42}' localhost:8080/users
```

### Reading documents at a path

This example reads all documents at the path `users`:

```bash
$ curl localhost:8080/users

{
  "John Doe": {
    "name": "John Doe",
    "age": 42
  }
}
```

### Deleting a document

This example deletes the document with key `John Doe` at the path `users`:

```bash
$ curl -X DELETE localhost:8080/users?key=John%20Doe
```

## Advanced Features - Zipping the content

Certain tools like [Open Policy Agent require the data to be zipped](https://www.openpolicyagent.org/docs/latest/management-bundles/#bundle-build) before they can be shipped. Pot supports zipping the content by setting the `zip` flag and providing the path to the zip file in it:

```bash
$ pot -bucket <bucket-name> -zip <zip-path>

# e.g. if you want to store the data in the bucket root in a file called bundle.zip:
$ pot -bucket <bucket-name> -zip .

# or if you want to store it in a subdirectory called bundle:
$ pot -bucket <bucket-name> -zip ./bundle
```

## Advanced Features - Using Pot as a Go Library

If you wish to embed Pot instead of using it as a binary, you can embed the library in your Go code by first installing the package:
  
```bash
$ go get github.com/petomalina/pot
```

Once the library is installed, you can import it and use the `NewClient` function to bootstrap the entry point to the library:

```go
package main

import (
  "context"

  "github.com/petomalina/pot"
)

func main() {
  ctx := context.Background()
  pot, err := pot.NewClient(context.Background(), "my-bucket")

  // pot.Create will create a document at the path `path/to/dir` with the key `John Doe`
  err = pot.Create(ctx, "path/to/dir", strings.NewReader("{\"name\": \"John Doe\", \"age\": 42}"))

  // pot.Get will return all documents at the path `path/to/dir` as a map[string]interface{}
  content, err = pot.Get(ctx, "path/to/dir")

  // pot.Delete will delete the document at the path `path/to/dir` with the key `John Doe`
  err = pot.Delete(ctx, "path/to/dir", "John Doe")
}
```