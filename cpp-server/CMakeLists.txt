cmake_minimum_required(VERSION 3.16)
project(mase_server LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

find_package(SQLite3 QUIET)
if (SQLite3_FOUND)
  set(MASE_SQLITE_LIB SQLite::SQLite3)
else ()
  find_path(SQLITE3_INCLUDE_DIR sqlite3.h)
  find_library(SQLITE3_LIBRARY NAMES sqlite3)
  if (NOT SQLITE3_INCLUDE_DIR OR NOT SQLITE3_LIBRARY)
    message(FATAL_ERROR "SQLite3 not found. Install libsqlite3-dev (Debian/Ubuntu).")
  endif ()
  add_library(SQLite::SQLite3 UNKNOWN IMPORTED)
  set_target_properties(SQLite::SQLite3 PROPERTIES
    IMPORTED_LOCATION "${SQLITE3_LIBRARY}"
    INTERFACE_INCLUDE_DIRECTORIES "${SQLITE3_INCLUDE_DIR}")
  set(MASE_SQLITE_LIB SQLite::SQLite3)
endif ()

add_executable(mase_server
    src/main.cpp
)

target_link_libraries(mase_server PRIVATE ${MASE_SQLITE_LIB} pthread)
