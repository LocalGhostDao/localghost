# Native build setup — on-phone llama.cpp (LocalGhost offline fallback)

The on-phone model is real native code. Unlike the rest of the app (framework-only Kotlin,
mocked through BoxClient), this needs the Android NDK, the llama.cpp source, and a GGUF model
file. None of it can be compiled or bundled here — these are the steps to make it build.

## 1. Add llama.cpp as a submodule
From the repo root:
    git submodule add https://github.com/ggerganov/llama.cpp app/src/main/cpp/llama.cpp
(Pin to a known-good tag; the JNI in llama_jni.cpp targets the current llama.h API:
llama_model_load_from_file, llama_init_from_model, llama_model_get_vocab, the sampler chain,
llama_batch_get_one, llama_token_to_piece. If you pin an older tag, adjust the JNI accordingly.)

## 2. app/build.gradle (or .kts) — enable the NDK build
android {
    defaultConfig {
        // keep your existing minSdk/targetSdk
        ndk {
            // 64-bit only; arm64 covers the S26 Ultra. Add x86_64 for the emulator.
            abiFilters += listOf("arm64-v8a")
        }
        externalNativeBuild {
            cmake { arguments += listOf("-DANDROID_STL=c++_shared") }
        }
    }
    externalNativeBuild {
        cmake {
            path = file("src/main/cpp/CMakeLists.txt")
            version = "3.22.1"
        }
    }
    // optional: shrink the APK by splitting per-ABI
    // splits { abi { isEnable = true; reset(); include("arm64-v8a"); isUniversalApk = false } }
}

Make sure the NDK is installed (Android Studio > SDK Manager > NDK (Side by side)) and, if
needed, set ndkVersion in the android { } block.

## 3. The model file (not bundled — too large)
LocalModel looks for the GGUF at:
    <app filesDir>/models/local.gguf
Sideload one there, or add a downloader. A small instruct model suits a phone, e.g. a
quantised Gemma 3 1B/2B or Qwen2.5 1.5B in Q4_K_M (~0.7–1.5 GB). To push one manually:
    adb push gemma-3-1b-it-Q4_K_M.gguf /sdcard/Download/
then have the app copy it into filesDir/models/local.gguf on first run (or wire a file picker).

LocalModel.isModelPresent() drives the UI: with no model, chat says "no on-phone model
installed" and the toggle is disabled; the box stays primary regardless.

## 4. What's already wired (Kotlin/JNI side, in this tree)
- app/src/main/cpp/CMakeLists.txt — builds llama.cpp + liblocalghost_llm.so
- app/src/main/cpp/llama_jni.cpp — JNI: load / free / streaming generate (cancellable)
- app/src/main/java/.../local/NativeLlama.kt — JNI binding + System.loadLibrary (safe if absent)
- app/src/main/java/.../local/LocalModel.kt — coroutine/Flow wrapper, State machine, model-path
- routing in MainActivity.sendChat: forceLocal || !BoxClient.reachable() -> on-phone model
- chat shows "no box — on-phone model, limited context"; sheet has a "use on-phone model" toggle

## 5. Honest caveats
- The native code cannot be verified in this environment; first real build may need small API
  tweaks against the exact llama.cpp tag you pin.
- The on-phone model has NO access to the life-index (that's the box). It answers generic
  questions only. This is the lifeboat, not the engine — by design.
- Inference on a phone is slow and battery-hungry for anything but small models; keep it 1–2B.

## 6. The box serves the models (not Hugging Face)
The phone does NOT fetch weights from the open internet. The box holds the GGUF files and
advertises which ones are small enough for the phone to run. Flow:
- ghost.secd exposes a model registry: list (BoxClient.availableModels) + byte stream with a
  resume offset (BoxClient.downloadModel(id, offset)).
- The phone shows the box's list, lets you download (from the box, over mTLS, resumable),
  activate, or delete the local copy. Delete removes the phone copy only; the box keeps it.
- Models land in filesDir/models/<id>.gguf on the phone.
On the box side you still need real GGUFs: pull community quants or convert with
llama.cpp/convert_hf_to_gguf.py + llama-quantize, store them on the box, and have ghost.secd
serve them. The app needs no Hugging Face access at all.

(If you'd rather run the exact Gallery LiteRT model, that's a different runtime — MediaPipe
LLM Inference / `com.google.mediapipe:tasks-genai` with a .task/.litertlm file, no NDK build.
We can add a LiteRT LocalModel implementation behind the same seam if you want both.)

## 7. Model download = WorkManager foreground service (built)
The download is a foreground-service WorkManager job (ModelDownloadWorker) that streams from the
box via BoxClient, survives the app closing / screen locking, shows a progress notification, and
resumes from the .part file. Manifest already declares FOREGROUND_SERVICE +
FOREGROUND_SERVICE_DATA_SYNC and the dataSync SystemForegroundService. No open-internet access —
bytes come from secd over the authenticated channel. (WorkManager dep: androidx.work:work-runtime-ktx,
which the app already uses for sync/poll.)
