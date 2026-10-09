# Like nocat, but without bash: shell-builtins with dash, which drops NUL bytes.
FROM mirror.gcr.io/library/debian:bookworm-slim
RUN rm -f /usr/bin/tar /usr/bin/cat /usr/bin/uname /usr/bin/bash && ! command -v tar cat uname bash
