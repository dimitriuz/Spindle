# Maintainer: Dmitrii Leshchenko (github.com/dimitriuz)
pkgname=cliamp-web
pkgver=0.1.0
pkgrel=1
pkgdesc="Spindle: web remote and library browser for cliamp (the cliamp-web bridge)"
arch=(x86_64 aarch64)
license=(MIT)
makedepends=(go)
source=("git+https://github.com/dimitriuz/Spindle#tag=v$pkgver")
sha256sums=("SKIP")
prepare() {
  cd Spindle
  go mod download
}
build() {
  cd Spindle
  make build
}
package() {
  install -Dm755 Spindle/dist/cliamp-web "$pkgdir/usr/bin/cliamp-web"
  install -Dm644 Spindle/LICENSE "$pkgdir/usr/share/licenses/$pkgname/LICENSE"
}
