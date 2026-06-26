// zig ships libc++ (std::__1::), but the prebuilt linux libtokenizers.a was
// built against GNU libstdc++ and pulls in two iostream static-init symbols via
// esaxx.cpp's <iostream> include. esaxx performs NO actual I/O (nm shows no
// std::cout/ostream refs), so the Init guard is dead weight. Provide the two
// GNU-mangled symbols as no-ops so zig can cross-link linux from macOS.
#if defined(__linux__)
void _ZNSt8ios_base4InitC1Ev(void) {}
void _ZNSt8ios_base4InitD1Ev(void) {}
#endif
