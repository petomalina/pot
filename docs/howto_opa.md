# Integrating Pot with Open Policy Agent

This guide shows you how to integrate Pot with [Open Policy Agent](https://www.openpolicyagent.org/). By the end of this guide you will have:
- A running Pot server that handles data changes and serves the data to OPA.
- A running OPA server that uses the data from Pot to make authorization decisions.

# Prerequisites

To install Pot, use the following command:

```bash
$ go install github.com/petomalina/pot@latest
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
```

## Local configuration of Open Policy Agent

If you wish to test Open Policy Agent server locally, you can use the following configuration and make sure that you replace:
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
    resource: 'bundle.tar.gz?alt=media'
    polling:
      min_delay_seconds: 3
      max_delay_seconds: 10
```

## Running the Open Policy Agent server locally

Then you can run the server with the following command:

```bash
$ opa run --server --log-level info --config-file ./config.yaml
```