cmake_minimum_required(VERSION 3.16)
project(llar_pkgconfig_consumer C)

find_package(PkgConfig REQUIRED)
pkg_check_modules(ZLIB REQUIRED IMPORTED_TARGET zlib)

# zlib.pc already names its LLAR output root. A target sysroot prepended to
# that absolute path would make both directories below nonexistent.
foreach(include_dir IN LISTS ZLIB_INCLUDE_DIRS)
  if(NOT EXISTS "${include_dir}/zlib.h")
    message(FATAL_ERROR "pkg-config returned missing zlib include directory: ${include_dir}")
  endif()
endforeach()
foreach(library_dir IN LISTS ZLIB_LIBRARY_DIRS)
  if(NOT EXISTS "${library_dir}/libz.a")
    message(FATAL_ERROR "pkg-config returned missing zlib library directory: ${library_dir}")
  endif()
endforeach()

add_executable(llar-pkgconfig-consumer main.c)
target_link_libraries(llar-pkgconfig-consumer PRIVATE PkgConfig::ZLIB)

install(TARGETS llar-pkgconfig-consumer RUNTIME DESTINATION bin)
