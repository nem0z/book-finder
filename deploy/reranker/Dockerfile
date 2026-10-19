# Text Embeddings Inference serving the re-ranker, with the model built in so a new container starts
# without downloading it. TEI_TAG picks the hardware: cpu-1.9 locally, turing-1.9 for an NVIDIA T4.
ARG TEI_TAG=cpu-1.9

# The model files at a fixed revision of https://huggingface.co/Alibaba-NLP/gte-reranker-modernbert-base.
# CPU builds also get the ONNX export: TEI runs it with ONNX Runtime, about 5 times faster on CPU than the
# safetensors weights. GPU builds use the safetensors weights and skip it (600 MB).
FROM curlimages/curl:latest AS model
# The image runs as a non-root user, and some builders (Azure Container Registry) create WORKDIR as root.
USER root
ARG TEI_TAG
ARG REPO=Alibaba-NLP/gte-reranker-modernbert-base
ARG REVISION=f7481e6055501a30fb19d090657df9ec1f79ab2c
WORKDIR /tmp/model
RUN files="config.json tokenizer.json tokenizer_config.json special_tokens_map.json model.safetensors"; \
    case "$TEI_TAG" in cpu-*) mkdir onnx; files="$files onnx/model.onnx";; esac; \
    for f in $files; do \
      curl -fsSL --retry 3 -o "$f" "https://huggingface.co/$REPO/resolve/$REVISION/$f" || exit 1; \
    done

FROM ghcr.io/huggingface/text-embeddings-inference:${TEI_TAG}
COPY --from=model /tmp/model /model
# TEI loads the model from this folder instead of the Hugging Face hub.
ENV MODEL_ID=/model
# Up to 100 texts per request: the API sends 30 candidates by default (TEI's own default is 32).
# float32 everywhere: on a T4, TEI defaults to float16, which degraded the ranking of this model (Rebecca and
# Legends & Lattes dropped out of first place on our test queries). CPU already uses float32.
CMD ["--max-client-batch-size", "100", "--dtype", "float32"]
