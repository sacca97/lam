plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

android {
    namespace = "org.lam.app"
    compileSdk = 36

    defaultConfig {
        applicationId = "org.lam.app"
        minSdk = 26
        targetSdk = 34
        // scripts/release.sh passes the tag; a plain build is the current release.
        versionName = (project.findProperty("lamVersion") as String?) ?: "0.2.0"
        versionCode = (project.findProperty("lamVersionCode") as String?)?.toInt() ?: 200
        ndk { abiFilters += "arm64-v8a" }
    }

    // A release is signed with the key scripts/release.sh keeps; without one
    // (a build on another machine) the debug key signs it for sideloading.
    val releaseKeystore = System.getenv("LAM_KEYSTORE")
    signingConfigs {
        if (releaseKeystore != null) {
            create("release") {
                storeFile = file(releaseKeystore)
                storePassword = System.getenv("LAM_KEYSTORE_PASSWORD")
                keyAlias = "lam"
                keyPassword = System.getenv("LAM_KEYSTORE_PASSWORD")
            }
        }
    }
    buildTypes {
        release {
            signingConfig = signingConfigs.getByName(if (releaseKeystore != null) "release" else "debug")
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlin {
        compilerOptions { jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17) }
    }
    buildFeatures { compose = true }
    packaging {
        jniLibs { useLegacyPackaging = false }
    }
}

dependencies {
    // Built by scripts/build-android.sh (gomobile bind ./mobile).
    implementation(files("libs/lammobile.aar"))
    // Termux terminal emulator and view (Apache-2.0).
    implementation("com.github.termux.termux-app:terminal-view:v0.118.3")
    implementation("androidx.drawerlayout:drawerlayout:1.2.0")

    val composeBom = platform("androidx.compose:compose-bom:2025.06.00")
    implementation(composeBom)
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-core")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-tooling-preview")
    debugImplementation("androidx.compose.ui:ui-tooling")
    implementation("androidx.activity:activity-compose:1.10.1")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.9.1")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.9.1")
    implementation("androidx.navigation:navigation-compose:2.9.0")
    testImplementation("junit:junit:4.13.2")
}
