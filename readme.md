# 🍲 Pot

Pot is an incredibly simple and lightweight implementation of a database based on [Cloud Storage](https://cloud.google.com/storage).
It lets you store, read, and delete any kind of structured data in your bucket.

## Running Pot

To run Pot, you need to have a Google Cloud Storage bucket. You can create one [here](https://console.cloud.google.com/storage/create-bucket). Pot then uses the local credentials to access the bucket. You can find more information about the credentials [here](https://cloud.google.com/docs/authentication/getting-started). Install pot using the Golang toolchain:

```bash
$ go install github.com/petomalina/pot/cmd/pot@latest
```

Pot requires only a single flag to run:

```bash
$ pot -bucket <bucket-name>
```

Pot runs by default on port `8080` and doesn't respect any other opinions on port selection. It is intended to be run in a serverless environment or an environment that supports port forwarding.

## Data Model

Pot stores data in a simple key-value store. The key must be a string and is always derived from the document that is being stored (either `id` or `name`). The value is always a JSON object. The structure of the file then looks like the following:

```json
{
  "John Doe": {
    "age": "42",
    "id": "John Doe"
  },
  ...
}
```

## Using Pot

Pot is a simple HTTP server that exposes three endpoints:
- `GET /<path>`: Returns the data stored at the given path.
- `POST /<path>`: Creates a new document at the given path. The body of the request is used as the data. Either `id` or `name` is used as the key of the document (`id` takes precedence).
- `DELETE /<path>?key=<key>`: Deletes the document at the given path with the given key.

Pot doesn't support any kind of filtering or querying a single document. Pot always returns all data on the given path. If you wish to store documents separately, you can use the `id` or `name` as the path.

## Examples

### Storing a document

This example stores a document with the key `John Doe`` at the path `users`:
Reading documents on a path

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

This example deletes the document with the key `John Doe`` at the path `users`:

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

  // pot.Zip will take contents of the whole bucket and zip them into a file at the given path
  err = pot.Zip(ctx, "path/to/bundle")
}
```

## Advanced Features - Using Distributed Lock

In scenarios where you aim to run Pot as a highly available service, you will need to use the distributed locking mechanism to ensure that only one Pot instance can write to the bucket path at a time. Pot uses [Cloud Storage's Object Generation](https://cloud.google.com/storage/docs/generations-preconditions) to implement the locking mechanism. This doubles the latency of each request but ensures that only one instance can write to the bucket path at a time.

> :warning: Pot uses local mutexes to queue the requests. If more instances happen to be acquiring the distributed lock, the second instance will receive a `412 Precondition Failed` errors. For this reason, it is recommended to run Pot with a single main instance with backup instances only in case of a failure.

To use the distributed lock, you need to set the `-distributed-lock` when starting Pot:

```bash
$ pot -bucket <bucket-name> -distributed-lock
```

You can now use Pot as usual. You will most likely see a performance drop in terms of latency since Pot now needs to make two roundtrips instead of just one. However, it will work just fine in the distributed environment.
