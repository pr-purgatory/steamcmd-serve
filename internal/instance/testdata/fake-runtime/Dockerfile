# Fake runtime image for integration tests: same entrypoint contract as steamcmd-base
# (install / run), no steamcmd. Build: make it-image
FROM docker.io/library/busybox:1.37
COPY entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
