#!/usr/bin/env bash
set -exo pipefail

ANKLET_TEMPLATE_UUID="84266873-da90-4e0d-903b-ed0233471f9f"

modify_uuid() {
  # Modify UUID (don't use in production; for getting-started demo only)
  [[ -z "$1" ]] && echo "no arguments... Please provide TEMPLATE_NAME as ARG1" && exit 1
  [[ -z "$2" ]] && echo "Please provided the new UUID as ARG2" && exit 2
  TEMPLATE_NAME=$1
  DEST_UUID=$2
  CUR_UUID=$(${SUDO} anka --machine-readable list | jq -r ".body[] | select(.name==\"$TEMPLATE_NAME\") | .uuid")
  if [[ -z "$(${SUDO} anka --machine-readable  registry list | jq ".body[] | select(.id == \"${DEST_UUID}\") | .name")" && "${CUR_UUID}" != "${DEST_UUID}" ]]; then
    ${SUDO} mv "$(${SUDO} anka config vm_lib_dir)/$CUR_UUID" "$(${SUDO} anka config vm_lib_dir)/$DEST_UUID"
    ${SUDO} sed -i '' "s/$CUR_UUID/$DEST_UUID/" "$(${SUDO} anka config vm_lib_dir)/$DEST_UUID/config.yaml"
  fi
}

# Pull base template
anka pull d792c6f6-198c-470f-9526-9c998efe7ab4 

# Clone base template
if ! anka list | grep -q "${ANKLET_TEMPLATE_UUID}"; then
  anka clone d792c6f6-198c-470f-9526-9c998efe7ab4 anklet
  modify_uuid anklet ${ANKLET_TEMPLATE_UUID}
fi

# Build tags

if ! anka registry show ${ANKLET_TEMPLATE_UUID} tag | grep -q "6c14r"; then
  anka modify ${ANKLET_TEMPLATE_UUID} cpu 6
  anka modify ${ANKLET_TEMPLATE_UUID} ram 14G
  anka push ${ANKLET_TEMPLATE_UUID} --tag 6c14r
fi

if ! anka registry show ${ANKLET_TEMPLATE_UUID} tag | grep -q "12c20r"; then
  anka modify ${ANKLET_TEMPLATE_UUID} cpu 12
  anka modify ${ANKLET_TEMPLATE_UUID} ram 20G
  anka push ${ANKLET_TEMPLATE_UUID} --tag 12c20r
fi

if ! anka registry show ${ANKLET_TEMPLATE_UUID} tag | grep -q "20c20r"; then
  anka modify ${ANKLET_TEMPLATE_UUID} cpu 20
  anka modify ${ANKLET_TEMPLATE_UUID} ram 20G
  anka push ${ANKLET_TEMPLATE_UUID} --tag 20c20r
fi


if ! anka registry show ${ANKLET_TEMPLATE_UUID} tag | grep -q "12c50r"; then
  anka modify ${ANKLET_TEMPLATE_UUID} cpu 12
  anka modify ${ANKLET_TEMPLATE_UUID} ram 50G
  anka push ${ANKLET_TEMPLATE_UUID} --tag 12c50r
fi