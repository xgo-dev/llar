cmake_minimum_required(VERSION 3.10)
project(cmaketest C)

add_library(cmtadd STATIC cmtadd.c)
target_include_directories(cmtadd PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})

install(TARGETS cmtadd ARCHIVE DESTINATION lib)
install(FILES cmtadd.h DESTINATION include)
