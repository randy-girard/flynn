CONFIG_JSON="/root/go/src/github.com/flynn/flynn/builder/manifest.json"
GO_FILE="/root/go/src/github.com/flynn/flynn/pkg/tufconfig/tufconfig.go"
ENV_FILE="/root/go/src/github.com/flynn/flynn/tup.config"

echo "==> Running tuf-repo to get root keys..."
RAW_OUTPUT=$(docker compose run --rm tuf-repo 2>/dev/null || true)

# Extract JSON array of keys from output
KEYS_JSON=$(echo "$RAW_OUTPUT" | grep -Eo '\[.*\]' | head -n1)

if [ -z "$KEYS_JSON" ]; then
  echo "❌ Error: Could not parse root keys from tuf-repo output."
  echo "Raw output was:"
  echo "$RAW_OUTPUT"
  exit 1
fi

echo "==> Parsed root keys:"
echo "$KEYS_JSON" | jq '.'

# Escape quotes for use inside Go string literal
ESCAPED_JSON=$(printf '%s' "$KEYS_JSON" | sed 's/"/\\"/g')

#######################################
# 1️⃣ Update config.json (.tuf.root_keys)
#######################################
if [ -f "$CONFIG_JSON" ]; then
  TMP_FILE=$(mktemp)
  jq --argjson keys "$KEYS_JSON" '.tuf.root_keys = $keys' "$CONFIG_JSON" > "$TMP_FILE"
  mv "$TMP_FILE" "$CONFIG_JSON"
  echo "✅ Updated $CONFIG_JSON"
else
  echo "⚠️  Skipped: $CONFIG_JSON not found"
fi

#######################################
# 2️⃣ Update Go config (RootKeysJSON var)
#######################################
if [ -f "$GO_FILE" ]; then
  sed -i "s|RootKeysJSON = \`.*\`|RootKeysJSON = \`$KEYS_JSON\`|" "$GO_FILE"
  echo "✅ Updated RootKeysJSON in $GO_FILE"
else
  echo "⚠️  Skipped: $GO_FILE not found"
fi

#######################################
# 3️⃣ Update .env file (CONFIG_TUF_ROOT_KEYS)
#######################################
if [ -f "$ENV_FILE" ]; then
  if grep -q '^CONFIG_TUF_ROOT_KEYS=' "$ENV_FILE"; then
    sed -i "s|^CONFIG_TUF_ROOT_KEYS=.*|CONFIG_TUF_ROOT_KEYS=$KEYS_JSON|" "$ENV_FILE"
  else
    echo "CONFIG_TUF_ROOT_KEYS=$KEYS_JSON" >> "$ENV_FILE"
  fi
  echo "✅ Updated CONFIG_TUF_ROOT_KEYS in $ENV_FILE"
else
  echo "⚠️  Skipped: $ENV_FILE not found"
fi

echo "🎉 All updates complete!"
