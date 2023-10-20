# Integrating Pot with Open Policy Agent

This guide shows you how to integrate Pot with [Open Policy Agent](https://www.openpolicyagent.org/). By the end of this guide you will have:
- A running Pot server that handles data changes and serves the data to OPA.
- A running OPA server that uses the data from Pot to make authorization decisions.

# Prerequisites

To install Pot, use the following command:

```bash
$ go install github.com/petomalina/pot/cmd/pot@latest
```

You can install OPA easily via `brew` or follow the [official guide](https://www.openpolicyagent.org/docs/latest/#1-download-opa).

```bash
$ brew install opa
```

Lastly, you will need a Google Cloud Project. You can create a trial account directly on [Google Cloud](https://cloud.google.com/gcp) or use your existing project if you have one.

To set up the `gcloud` project, run:
  
```bash
$ gcloud config set project <your-project>
```

## Creating a Bucket and a Service Account

You will need a bucket to store the data for OPA and a Service Account that will be used to access the bucket. We will be creating only a single Service Account in this guide, however, if you wish to run this in production, you should create a separate Service Account for Pot instance and OPA instance. While the Pot instance should be able to read and write to the bucket, OPA only needs read permissions.

To create a bucket with your default gcloud settings, simply run the command below or check out [the official guide](https://cloud.google.com/storage/docs/creating-buckets#storage-create-bucket-cli):

```bash
$ gcloud storage buckets create gs://<bucket-name>
```

To create a Service Account, you can again check out the [official guide](https://cloud.google.com/iam/docs/service-accounts-create) or run:

```bash
$ gcloud iam service-accounts create <your-sa-name>

# add required permissions
$ gcloud storage buckets add-iam-policy-binding <bucket-name> --member serviceAccount:<your-sa-name>@<your-project>.iam.gserviceaccount.com --role roles/storage.objectUser
```

## Running Pot

Before you can run Pot, you will need to activate the service account you created in the previous step. You can do so by running:

```bash
$ gcloud auth activate-service-account <your-sa-name>@<your-project>.iam.gserviceaccount.com --key-file=<path-to-your-private-key>
```

Then you can run Pot with the following command and you should see this output:

```bash
$ pot -bucket <bucket-name>

2023/10/20 16:37:32 INFO starting server on :8080
```

Lastly, we will test that Pot works correctly. We will store 2 documents on the path `landmarks`:
  
```bash
$ curl -X POST -d '{"id": "sagrada-familia", "age": 141}' localhost:8080/landmarks
$ curl -X POST -d '{"id": "eiffel-tower", "age": 136}' localhost:8080/landmarks
```

And then we will read the documents back:

```bash
$ curl localhost:8080/landmarks

{
  "eiffel-tower": {
    "age": 136,
    "id": "eiffel-tower"
  },
  "sagrada-familia": {
    "age": 141,
    "id": "sagrada-familia"
  }
}
```

## Running Open Policy Agent locally

To run Open Policy Agent server locally, you can use the following configuration and make sure that you replace:
- `<your-bucket-name>` with the name of the bucket you wish to use.
- `<your-project>` with the name of your GCP project that hosts the service account.
- `<your-sa-name>` with the name of the service account you wish to use (needs read access to the bucket).
- `<your-sa-private-key>` with the private key of the service account provided. Follow the [official guide](https://cloud.google.com/iam/docs/keys-create-delete#creating) to create a new JSON key and copy-paste the private key from it.

```yaml
# config.yaml
services:
  gcs:
    url: https://storage.googleapis.com/storage/v1/b/<your-bucket-name>/o
    credentials:
      oauth2:
        token_url: https://oauth2.googleapis.com/token
        grant_type: jwt_bearer
        signing_key: gcs_signing_key
        scopes:
        - https://www.googleapis.com/auth/devstorage.read_only
        additional_claims:
          aud: https://oauth2.googleapis.com/token
          iss: <your-sa-name>@<your-project>.iam.gserviceaccount.com

keys:
  gcs_signing_key:
    algorithm: RS256
    private_key: |
      -----BEGIN PRIVATE KEY-----
      <your-sa-private-key>
      -----END PRIVATE KEY-----

bundles:
  authz:
    service: gcs
    resource: 'bundles%2Fbundle.tar.gz?alt=media'
    polling:
      min_delay_seconds: 3
      max_delay_seconds: 10
```

Then you can run the server with the following command:

```bash
$ opa run --server --log-level info --config-file ./config.yaml
```

## Integrating Pot and OPA

Now that we have both Pot and OPA running, we can integrate them together. We will be using the [bundle API](https://www.openpolicyagent.org/docs/latest/management/#bundles) to integrate the two. The bundle API allows us to upload a tarball with the data to OPA and then OPA will periodically download the tarball and update the data. This allows us to use Pot as a data source for OPA.

To create the tarball automatically, we will restart the Pot server and provide the `-zip` flag, which creates tarballs on changes to the Pot bucket. The following command will store the tarball in the `bundles` directory:

```bash
$ pot -bucket <bucket-name> -zip bundles
```

Since OPA evaluates policies based on the data we give it, we will need to create a simple policy. The policy will check whether the user is right about the age of the landmark. The policy is stored in the `policies` directory:

```rego
# policies/landmarks.rego
package landmarks

default allow = false

allow {
  input.age == data.landmarks[input.id].age
}
```

Lastly, we will upload this policy to the same bucket on the path `policies/landmarks.rego`:

```bash
$ gsutil cp policies/landmarks.rego gs://<bucket-name>/policies/landmarks.rego
```
