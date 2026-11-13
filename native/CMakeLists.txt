cmake_minimum_required(VERSION 3.22)
project(elysium_native LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

# Strict warnings apply to project targets only. Vendored code stays
# outside these flags through a SYSTEM include path.
set(ELYSIUM_WARNINGS -Wall -Wextra -Wpedantic -Wconversion -Wsign-conversion -Wshadow -Werror)

add_library(elysium_sync src/sync/lag_detector.cpp)
target_include_directories(elysium_sync PUBLIC include)
target_compile_options(elysium_sync PRIVATE ${ELYSIUM_WARNINGS})

include(CTest)
if(BUILD_TESTING)
  add_executable(elysium_native_tests tests/test_main.cpp tests/lag_detector_test.cpp)
  target_include_directories(elysium_native_tests SYSTEM PRIVATE third_party/doctest)
  target_link_libraries(elysium_native_tests PRIVATE elysium_sync)
  target_compile_options(elysium_native_tests PRIVATE ${ELYSIUM_WARNINGS})
  add_test(NAME elysium_native_tests COMMAND elysium_native_tests)
endif()
