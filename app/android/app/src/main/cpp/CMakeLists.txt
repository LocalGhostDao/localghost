cmake_minimum_required(VERSION 3.22.1)
project("localghost_llm")

# llama.cpp is expected at app/src/main/cpp/llama.cpp (git submodule or copied source).
# git submodule add https://github.com/ggerganov/llama.cpp app/src/main/cpp/llama.cpp
set(LLAMA_BUILD_TESTS    OFF CACHE BOOL "" FORCE)
set(LLAMA_BUILD_EXAMPLES OFF CACHE BOOL "" FORCE)
set(LLAMA_BUILD_SERVER   OFF CACHE BOOL "" FORCE)
set(GGML_OPENMP          OFF CACHE BOOL "" FORCE)   # avoid OpenMP on Android

add_subdirectory(${CMAKE_CURRENT_SOURCE_DIR}/llama.cpp ${CMAKE_BINARY_DIR}/llama.cpp)

add_library(localghost_llm SHARED llama_jni.cpp)

target_include_directories(localghost_llm PRIVATE
    ${CMAKE_CURRENT_SOURCE_DIR}/llama.cpp/include
    ${CMAKE_CURRENT_SOURCE_DIR}/llama.cpp/ggml/include
)

find_library(log-lib log)
target_link_libraries(localghost_llm llama ${log-lib})
