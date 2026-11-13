ARG PYTHON_BASE_IMAGE=public.ecr.aws/docker/library/python@sha256:f8417a152ae83c696bee8861ed715acd93a9c9a925cd868325bc0afd788537e6
FROM ${PYTHON_BASE_IMAGE}
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
COPY deploy/docker/governance-code/python.py /opt/datamesh/python.py
USER 65532:65532
ENTRYPOINT ["python3","-I","-u","/opt/datamesh/python.py"]
