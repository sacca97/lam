# Source me: env for cgo-cross-compiling lam code to android/arm64.
DEPS="${LAM_DEPS:-$HOME/.cache/lam}"
export ANDROID_HOME="${ANDROID_HOME:-$HOME/Android/Sdk}"
export ANDROID_NDK_HOME="${ANDROID_NDK_HOME:-$ANDROID_HOME/ndk/28.2.13676358}"
export ANDROID_API="${ANDROID_API:-26}"
NDK_BIN="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin"
export PKG_CONFIG_PATH="$DEPS/libghostty-android-arm64/share/pkgconfig"
export GOOS=android GOARCH=arm64 CGO_ENABLED=1
export CC="$NDK_BIN/aarch64-linux-android${ANDROID_API}-clang"
