# Verifying a LocalGhost build

LocalGhost is open source. You do not have to trust us, you can check. This proves the APK on
your phone was built from a specific, unmodified commit, by us, and signed with our key.

The app's **VERIFY BUILD** screen (menu → VERIFY BUILD) shows everything below and lets you copy
each value and command. You can follow it entirely from the app + your PC.

## The provenance chain
Three independent links. Any one is useful; together they are conclusive.

Signed by **info@localghost.ai** (same key as the website deploy manifest). Public key:
https://www.localghost.ai/.well-known/pgp-key.asc

1. **Source integrity**, `ghost/source-manifest.txt` lists every source file and its SHA-256.
   `MANIFEST.root` is the SHA-256 of that manifest (one value fixing the whole tree).
   `ghost/source-manifest.txt.asc` is our detached GPG signature over the manifest. → proves "these exact
   files, signed by us."
2. **Source → binary**, the build is stamped with the git commit and is reproducible, so
   rebuilding yields the same APK. → proves "this source produced this binary."
3. **Binary authenticity**, the APK's v2/v3 signature (keystore, verified by `apksigner`) plus
   a detached GPG signature over the APK by **info@localghost.ai**, the same key that signs the
   website and the source manifest. → proves "we, one identity, signed this exact binary."

## What the app shows
- **built (UTC)** and **version**
- **source commit** (full SHA) + **working tree at build** (clean / DIRTY)
- **source manifest root** (must equal what `tools/verify_source.sh` prints)
- **signing cert SHA-256** (read live from the system)
- links to the source at that commit, the signed manifest, and the release

## Verify on your PC

### 1. Get the exact source
```
git clone https://github.com/<org>/localghost-app
cd localghost-app
git checkout <commit>            # the commit shown in the app
git status --porcelain           # must print nothing (clean tree)
```

### 2. Verify the source matches our signed manifest
Import our public key once (the same key used on the website):
```
curl https://www.localghost.ai/.well-known/pgp-key.asc | gpg --import
```
Then:
```
gpg --verify ghost/source-manifest.txt.asc ghost/source-manifest.txt    # signature must be valid + our key
tools/verify_source.sh                            # re-hashes every file
```
The "root hash" printed must equal **source manifest root** in the app. A valid signature plus
matching hashes means the source is exactly what we signed, not one byte changed.

### 3. Rebuild and compare the APK
```
./gradlew assembleRelease
sha256sum app/build/outputs/apk/release/app-release.apk
```
Compare to the APK SHA-256 in the GitHub Release notes for this version.

### 4. Compare to the APK on your phone
```
adb shell pm path com.localghost.app
adb pull <printed path> phone.apk
sha256sum phone.apk
```
Same hash as step 3 → the running binary is exactly the source you just verified.

### 5. Confirm our signature
```
apksigner verify --print-certs phone.apk
```
The SHA-256 certificate digest must equal **signing cert SHA-256** in the app and the one we
publish. This is what stops a modified fork shipping under our name.

### 6. Confirm our GPG identity vouches for the binary
Download `app-release.apk.asc` from the release (next to the APK), then:
```
gpg --verify app-release.apk.asc app-release.apk
```
A good signature from **info@localghost.ai** ties this exact binary to the same identity that
signs our website and source manifest. One key vouches for the whole chain.

## Releaser steps (us, at build time)
```
git add -A && git commit -m "build: ..."     # clean tree
tools/sign_source.sh                          # writes manifest + .asc + MANIFEST.root (signs with info@localghost.ai)
git add ghost/source-manifest.txt ghost/source-manifest.txt.asc MANIFEST.root && git commit -m "build: manifest"
./gradlew assembleRelease                       # stamps commit + manifest root into the APK
sha256sum app/build/outputs/apk/release/app-release.apk   # publish in the release notes
gpg --batch --yes --armor --local-user info@localghost.ai \
    --output app/build/outputs/apk/release/app-release.apk.asc \
    --detach-sign app/build/outputs/apk/release/app-release.apk
```
Publish in the GitHub Release: the commit SHA, the APK SHA-256, our signing cert SHA-256, and
the manifest root. Attach both the APK and its `.asc` GPG signature. We only release clean-tree builds.

Trust is not asked for. It is checked.
