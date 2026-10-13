xgo 1.6

project *_llar.gox ModuleF github.com/goplus/llar/runtime/formula

import github.com/goplus/llar/runtime/x/autotools
import github.com/goplus/llar/runtime/x/cmake
import github.com/goplus/llar/runtime/x/pkgconfig

project *_cmp.gox CmpApp github.com/goplus/llar/runtime/cmp

import golang.org/x/mod/semver
import github.com/goplus/llar/runtime/x/gnu
