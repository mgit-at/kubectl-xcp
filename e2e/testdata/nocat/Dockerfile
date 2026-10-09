# Only bash and coreutils without tar, cat and uname: binary-safe shell-builtins.
FROM mirror.gcr.io/library/debian:bookworm-slim
RUN rm -f /usr/bin/tar /usr/bin/cat /usr/bin/uname && ! command -v tar cat uname
