FROM squidfunk/mkdocs-material
COPY requirements.txt .
RUN pip install -r requirements.txt
ENTRYPOINT []
CMD []
RUN git config --global user.name kcp-docs-bot; \
    git config --global user.email no-reply@kcp.io
