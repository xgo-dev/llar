cmake_minimum_required(VERSION 3.15)
project(dummy C)

add_library(dummy STATIC dummy.c)
target_include_directories(dummy PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)

install(TARGETS dummy ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
