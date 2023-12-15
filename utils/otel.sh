#!/bin/bash

docker run -it --rm \
  -v $(pwd)/config.yaml:/etc/otelcol-contrib/config.yaml \
  -v $(pwd)/tmp/pot-sandbox.json:/etc/otel/key.json \
  -e GOOGLE_APPLICATION_CREDENTIALS=/etc/otel/key.json \
  -p 4317:4317 \
  -p 55681:55681 \
  otel/opentelemetry-collector-contrib:0.91.0
