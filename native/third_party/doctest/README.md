# doctest 2.5.3 (vendored)

Single-header C++ test framework for the native core tests.

- Upstream: `https://github.com/doctest/doctest`
- Version: 2.5.3
- Source of this copy: Ubuntu archive package `doctest-dev` 2.5.3-1
  (`archive.ubuntu.com/ubuntu/pool/universe/d/doctest/`), extracted with
  `dpkg-deb -x`
- `doctest.h` SHA-256:
  `cfd518a3ef90f67e1f3ba514df23fb3627437de1a2feeba78cf5062a40021421`
- License: MIT (Expat), see `LICENSE.txt`

Version 2.5.3 includes `<version>` instead of `<ciso646>` under clang in
C++17 and later. libstdc++ 15 and newer libc++ warn on `<ciso646>` in
C++20.

Do not modify `doctest.h`. Replace it whole when upgrading.
