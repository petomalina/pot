# Running Pot on Cloud Run

```sh
gcloud secrets create otel-config --data-file=otel-config.yaml
```

```sh
export SERVICE_ACCOUNT_EMAIL="your-service-account"
envsubst < service.yaml | gcloud run services replace -
```