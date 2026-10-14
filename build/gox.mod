xgo 1.6

project *_llar.gox ModuleF github.com/goplus/llar/build/runtime/formula

import github.com/goplus/llar/build/runtime/x/autotools
import github.com/goplus/llar/build/runtime/x/cmake
import github.com/goplus/llar/build/runtime/x/pkgconfig

project *_cmp.gox CmpApp github.com/goplus/llar/build/runtime/cmp

import golang.org/x/mod/semver
import github.com/goplus/llar/build/runtime/x/gnu
