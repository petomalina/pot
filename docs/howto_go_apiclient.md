# Integrating Go API Client with Pot Server

In this guide, you will learn how to call Pot server from your Go application. Pot
comes with a Golang API Client library that can be used to call Pot server in a 
typesafe manner.

## Installing the library

The library is part of the same package as the Pot server. You can install it by
running:

```bash
$ go get github.com/petomalina/pot
```

## Using the library

The library exposes a single constructor `NewAPIClient[T]` that returns a new API Client. Since the library is typed, you need to specify the type of the object you will be working with. The type must implement the `Key()` method, which returns the key of the object. This is because the API Client implements creation using the batching API, and so won't derive the key from the object itself.

If you want to use the library with multiple types, create multiple instances of the client.

```go
package main

import (
  "github.com/petomalina/pot"
)

type Permission struct {
  ID string `json:"name"`
  Subject string `json:"subject"`
  Role string `json:"role"`
}

func (p Permission) Key() string {
  return p.ID
}

func main() {
  client := pot.NewAPIClient<Perission>("http://localhost:8080")
  // ...
}
```

The client exposes the same methods as the Pot server. In the example below, we 
will be working with the path `projects/myproject`, which is individually stored
on the bucket when using Pot. The permissions will then be stored on this path.

```go
// Create a new permission
err := client.Create("projects/myproject", Permission{
  ID: "user:petomalina:admin",
  Subject: "petomalina",
  Role: "admin",
})

// Get all permissions
permissions, err := client.Get("projects/myproject")

// Delete a permission
err := client.Remove("projects/myproject", "user:petomalina:admin")
```