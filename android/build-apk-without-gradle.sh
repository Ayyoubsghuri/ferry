#!/usr/bin/env bash
# Builds the APK with bare Android tools (no Gradle). Normally use Gradle/Android Studio or
# the GitHub workflow instead; this exists for environments where Google's Maven is unreachable.
#
# Needs: ANDROID_JAR (platform android.jar, API 35), AAPT2, DX_JAR (dx) or D8 (d8 binary),
#        APKSIGNER_JAR (or apksigner on PATH), Go 1.22+, JDK 17+.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd .. && pwd)
OUT=${OUT:-$ROOT/dist}
VERSION=${VERSION:-3.2.0}
VCODE=${VCODE:-30200}
B=build/manual
rm -rf "$B" && mkdir -p "$B"/{res,classes,apk} "$OUT"

echo "› Server binaries"
JNI=app/src/main/jniLibs
mkdir -p $JNI/arm64-v8a $JNI/armeabi-v7a $JNI/x86_64
( cd "$ROOT"
  CGO_ENABLED=0 GOOS=android GOARCH=arm64       go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o android/$JNI/arm64-v8a/libferry.so .
  CGO_ENABLED=0 GOOS=linux   GOARCH=arm GOARM=7 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o android/$JNI/armeabi-v7a/libferry.so .
  CGO_ENABLED=0 GOOS=linux   GOARCH=amd64       go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o android/$JNI/x86_64/libferry.so . )

echo "› Resources"
"$AAPT2" compile --dir app/src/main/res -o "$B/res/compiled.zip"
# Gradle adds these from build.gradle (namespace, packaging.jniLibs.useLegacyPackaging).
sed -e 's#<manifest #<manifest package="app.ferry" #' \
    -e 's#<application#<application android:extractNativeLibs="true"#' \
    app/src/main/AndroidManifest.xml > "$B/AndroidManifest.xml"
"$AAPT2" link -o "$B/base.apk" -I "$ANDROID_JAR" --manifest "$B/AndroidManifest.xml" \
  --min-sdk-version 24 --target-sdk-version 35 --version-code "$VCODE" --version-name "$VERSION" \
  "$B/res/compiled.zip"

echo "› Code"
javac -nowarn --release 8 -cp "$ANDROID_JAR" -d "$B/classes" $(find app/src/main/java -name '*.java')
if [ -n "${D8:-}" ]; then
  "$D8" --min-api 24 --lib "$ANDROID_JAR" --output "$B" $(find "$B/classes" -name '*.class')
else
  java -jar "$DX_JAR" --dex --min-sdk-version=24 --output="$B/classes.dex" "$B/classes"
fi

echo "› Package"
cp "$B/base.apk" "$B/unsigned.apk"
( cd "$B" && zip -q -X unsigned.apk classes.dex )
( cd app/src/main/jniLibs && find . -name '*.so' | sed 's#^\./##' | while read -r so; do
    mkdir -p "$OLDPWD/$B/apk/lib/$(dirname "$so")"; cp "$so" "$OLDPWD/$B/apk/lib/$so"; done )
( cd "$B/apk" && zip -q -X -r ../unsigned.apk lib )

echo "› Sign"
SIGN=(java -jar "$APKSIGNER_JAR")
command -v apksigner >/dev/null && [ -z "${APKSIGNER_JAR:-}" ] && SIGN=(apksigner)
"${SIGN[@]}" sign --ks keystore/ferry.jks --ks-pass pass:ferryapp --key-pass pass:ferryapp \
  --ks-key-alias ferry --min-sdk-version 24 --out "$OUT/Ferry-$VERSION.apk" "$B/unsigned.apk"
"${SIGN[@]}" verify --min-sdk-version 24 "$OUT/Ferry-$VERSION.apk"
echo "✓ $OUT/Ferry-$VERSION.apk"
