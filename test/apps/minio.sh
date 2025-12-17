#!/bin/bash

set -e

apt-get update
apt-get install -y wget rpm # for shasum

#URL="https://dl.minio.io/server/minio/release/linux-amd64/archive/minio.RELEASE.2019-05-23T00-29-34Z"
#SHA="6d791cba42ef3e9b8c807715b5b4d3bc8cecf40bcec93be5b50f89429fedc457"

#TMP="$(mktemp --directory)"
#trap "rm -rf ${TMP}" EXIT

#curl -fsSLo "${TMP}/minio" "${URL}"
#echo "${SHA}  ${TMP}/minio" | shasum -a 256 -c

#mv "${TMP}/minio" "/bin/minio"
#chmod +x "/bin/minio"

wget -O /tmp/minio-0.0.20210116021944.x86_64.rpm https://dl.min.io/server/minio/release/linux-amd64/archive/minio-0.0.20210116021944.x86_64.rpm
rpm -ivh /tmp/minio-0.0.20210116021944.x86_64.rpm
rm -rf /tmp/minio-0.0.20210116021944.x86_64.rpm
