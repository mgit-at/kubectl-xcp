# Neither tar nor uname: inject fails, downloads use the shell strategy.
FROM debian:bookworm-slim
RUN rm -f /usr/bin/tar /bin/tar /usr/bin/uname /bin/uname && ! command -v tar && ! command -v uname
