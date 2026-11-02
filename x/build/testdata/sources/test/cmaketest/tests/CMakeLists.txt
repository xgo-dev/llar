cmake_minimum_required(VERSION 3.10)
project(cmaketest_check C)

# CMAKE_PREFIX_PATH is injected by llar's cmake.use(installDir) so that
# find_library / find_path discover the library that onBuild just installed.
find_library(CMTADD_LIB NAMES cmtadd REQUIRED)
find_path(CMTADD_INCLUDE cmtadd.h REQUIRED)

message(STATUS "cmaketest_check linking against: ${CMTADD_LIB}")
message(STATUS "cmaketest_check includes from:   ${CMTADD_INCLUDE}")

add_executable(cmtadd_check check.c)
target_include_directories(cmtadd_check PRIVATE ${CMTADD_INCLUDE})
target_link_libraries(cmtadd_check PRIVATE ${CMTADD_LIB})
