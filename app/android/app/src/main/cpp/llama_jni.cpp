#include <jni.h>
#include <android/log.h>
#include <string>
#include <vector>
#include "llama.h"

#define LOG_TAG "localghost_llm"
#define LOGI(...) __android_log_print(ANDROID_LOG_INFO,  LOG_TAG, __VA_ARGS__)
#define LOGE(...) __android_log_print(ANDROID_LOG_ERROR, LOG_TAG, __VA_ARGS__)

// Opaque handle bundling model + context, returned to Kotlin as a long.
struct LgLlm {
    llama_model*   model = nullptr;
    llama_context* ctx   = nullptr;
    const llama_vocab* vocab = nullptr;
};

extern "C" JNIEXPORT jlong JNICALL
Java_com_localghost_app_local_NativeLlama_nativeLoad(
        JNIEnv* env, jobject /*thiz*/, jstring jModelPath, jint nCtx, jint nThreads) {
    const char* path = env->GetStringUTFChars(jModelPath, nullptr);

    llama_backend_init();

    llama_model_params mparams = llama_model_default_params();
    // n_gpu_layers stays 0 on phone unless a Vulkan/OpenCL backend is compiled in.
    llama_model* model = llama_model_load_from_file(path, mparams);
    env->ReleaseStringUTFChars(jModelPath, path);
    if (!model) { LOGE("model load failed"); return 0; }

    llama_context_params cparams = llama_context_default_params();
    cparams.n_ctx     = (uint32_t) nCtx;
    cparams.n_threads = nThreads;
    cparams.n_threads_batch = nThreads;
    llama_context* ctx = llama_init_from_model(model, cparams);
    if (!ctx) { LOGE("ctx init failed"); llama_model_free(model); return 0; }

    auto* h = new LgLlm{model, ctx, llama_model_get_vocab(model)};
    LOGI("model loaded");
    return reinterpret_cast<jlong>(h);
}

extern "C" JNIEXPORT void JNICALL
Java_com_localghost_app_local_NativeLlama_nativeFree(
        JNIEnv* /*env*/, jobject /*thiz*/, jlong handle) {
    auto* h = reinterpret_cast<LgLlm*>(handle);
    if (!h) return;
    if (h->ctx)   llama_free(h->ctx);
    if (h->model) llama_model_free(h->model);
    delete h;
    llama_backend_free();
}

// Generate from prompt, streaming each piece back through callback.onToken(String):Boolean.
// Returning false from Kotlin cancels generation (used by the STOP button).
extern "C" JNIEXPORT void JNICALL
Java_com_localghost_app_local_NativeLlama_nativeGenerate(
        JNIEnv* env, jobject /*thiz*/, jlong handle, jstring jPrompt,
        jint maxTokens, jobject callback) {
    auto* h = reinterpret_cast<LgLlm*>(handle);
    if (!h) return;

    jclass cbClass = env->GetObjectClass(callback);
    jmethodID onToken = env->GetMethodID(cbClass, "onToken", "(Ljava/lang/String;)Z");

    const char* prompt = env->GetStringUTFChars(jPrompt, nullptr);
    std::string promptStr(prompt);
    env->ReleaseStringUTFChars(jPrompt, prompt);

    // tokenize
    const int n_prompt = -llama_tokenize(h->vocab, promptStr.c_str(), promptStr.size(),
                                         nullptr, 0, true, true);
    std::vector<llama_token> tokens(n_prompt);
    if (llama_tokenize(h->vocab, promptStr.c_str(), promptStr.size(),
                       tokens.data(), tokens.size(), true, true) < 0) {
        LOGE("tokenize failed"); return;
    }

    // greedy sampler chain
    llama_sampler* smpl = llama_sampler_chain_init(llama_sampler_chain_default_params());
    llama_sampler_chain_add(smpl, llama_sampler_init_min_p(0.05f, 1));
    llama_sampler_chain_add(smpl, llama_sampler_init_temp(0.8f));
    llama_sampler_chain_add(smpl, llama_sampler_init_dist(LLAMA_DEFAULT_SEED));

    llama_batch batch = llama_batch_get_one(tokens.data(), tokens.size());

    int generated = 0;
    char piece[256];
    while (generated < maxTokens) {
        if (llama_decode(h->ctx, batch)) { LOGE("decode failed"); break; }

        llama_token id = llama_sampler_sample(smpl, h->ctx, -1);
        if (llama_vocab_is_eog(h->vocab, id)) break;

        int n = llama_token_to_piece(h->vocab, id, piece, sizeof(piece), 0, true);
        if (n < 0) break;
        jstring jPiece = env->NewStringUTF(std::string(piece, n).c_str());
        jboolean cont = env->CallBooleanMethod(callback, onToken, jPiece);
        env->DeleteLocalRef(jPiece);
        if (!cont) break;   // Kotlin asked to stop

        batch = llama_batch_get_one(&id, 1);
        generated++;
    }

    llama_sampler_free(smpl);
}
