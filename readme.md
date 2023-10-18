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