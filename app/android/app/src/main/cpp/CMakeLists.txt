cmake_minimum_required(VERSION 3.22.1)
project("localghost_llm")

# llama.cpp is our ONLY external native dependency. It is pinned to an exact, IMMUTABLE upstream
# commit (not a tag, which can be re-pointed) and fetched at configure time. After fetch, CMake
# verifies the checked-out commit matches the pin and fails the build on any mismatch. The pin
# here is the single source of truth: the setup script, the build-env provenance, and the deploy
# version check all read these two lines.
#
# Repo: https://github.com/ggml-org/llama.cpp
# LLAMA_CPP_TAG is the human-readable build number; LLAMA_CPP_COMMIT is what's actually fetched
# and verified. Keep both pointing at the same release when you bump.
set(LLAMA_CPP_TAG    "b9788" CACHE STRING "llama.cpp release tag (for humans)")
set(LLAMA_CPP_COMMIT "e9fb3b3fc0300501179b0ce61e907be767e8c859" CACHE STRING "llama.cpp pinned commit (verified)")

set(LLAMA_BUILD_TESTS    OFF CACHE BOOL "" FORCE)
set(LLAMA_BUILD_EXAMPLES OFF CACHE BOOL "" FORCE)
set(LLAMA_BUILD_SERVER   OFF CACHE BOOL "" FORCE)
set(GGML_OPENMP          OFF CACHE BOOL "" FORCE)   # avoid OpenMP on Android

include(FetchContent)
FetchContent_Declare(
    llama_cpp
    GIT_REPOSITORY https://github.com/ggml-org/llama.cpp.git
    GIT_TAG        ${LLAMA_CPP_COMMIT}   # full commit SHA: immutable, content-addressed
)
FetchContent_MakeAvailable(llama_cpp)

# Verify the fetched source is exactly the pinned commit. FetchContent checks out the SHA, but we
# assert it explicitly so a tampered cache or a redirected fetch fails the build loudly.
FetchContent_GetProperties(llama_cpp SOURCE_DIR LLAMA_SRC)
execute_process(
    COMMAND git -C "${LLAMA_SRC}" rev-parse HEAD
    OUTPUT_VARIABLE LLAMA_ACTUAL_COMMIT
    OUTPUT_STRIP_TRAILING_WHITESPACE
)
if(NOT LLAMA_ACTUAL_COMMIT STREQUAL LLAMA_CPP_COMMIT)
    message(FATAL_ERROR
        "llama.cpp commit mismatch.\n"
        "  pinned:   ${LLAMA_CPP_COMMIT}\n"
        "  fetched:  ${LLAMA_ACTUAL_COMMIT}\n"
        "Refusing to build against unverified llama.cpp source.")
endif()
message(STATUS "llama.cpp verified at ${LLAMA_CPP_COMMIT} (tag ${LLAMA_CPP_TAG})")

add_library(localghost_llm SHARED llama_jni.cpp)

find_library(log-lib log)
target_link_libraries(localghost_llm llama ${log-lib})
