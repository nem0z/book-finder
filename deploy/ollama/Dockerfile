# Ollama with the EmbeddingGemma model built in, for embedding search queries on CPU. The official image is
# ~9 GB, mostly NVIDIA and Apple GPU libraries; this one keeps only the CPU runtime and the model, so new
# containers (locally or in the cloud) start quickly and never download the model.
ARG OLLAMA_VERSION=0.40.4

FROM ollama/ollama:${OLLAMA_VERSION} AS full
ARG MODEL=embeddinggemma
# Pulling needs a running server: start one for the build, wait until it answers, pull, stop it.
RUN ollama serve & pid=$!; \
    for i in $(seq 1 30); do ollama list >/dev/null 2>&1 && break; sleep 1; done; \
    ollama pull "$MODEL" && kill "$pid"
RUN rm -rf /usr/lib/ollama/cuda_* /usr/lib/ollama/mlx_* /usr/lib/ollama/rocm* /usr/lib/ollama/vulkan

# Same base as the official image.
FROM ubuntu:24.04
COPY --from=full /usr/bin/ollama /usr/bin/ollama
COPY --from=full /usr/lib/ollama /usr/lib/ollama
COPY --from=full /root/.ollama /root/.ollama
ENV OLLAMA_HOST=0.0.0.0:11434
EXPOSE 11434
ENTRYPOINT ["/usr/bin/ollama"]
CMD ["serve"]
