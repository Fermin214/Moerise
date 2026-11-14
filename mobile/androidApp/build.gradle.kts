import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.compose.compiler)
}
android {
    namespace = "io.github.fermin214.moerise"
    compileSdk = 36
    defaultConfig {
        applicationId = "io.github.fermin214.moerise"
        minSdk = 26
        targetSdk = 36
        versionCode = 1
        versionName = "0.1.0"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }
    buildFeatures { compose = true; buildConfig = true }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    buildTypes {
        create("fixture") {
            initWith(getByName("debug"))
            applicationIdSuffix = ".fixture"
            matchingFallbacks += "debug"
        }
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }
    testBuildType = "fixture"
    sourceSets.getByName("fixture").res.srcDir("src/debug/res")
}
kotlin { compilerOptions.jvmTarget.set(JvmTarget.JVM_17) }
dependencies {
    implementation(project(":shared"))
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.core)
    implementation(libs.androidx.viewmodel)
    implementation(libs.room.runtime)
    implementation(libs.androidx.tracing)
    androidTestImplementation(libs.androidx.test.runner)
    androidTestImplementation(libs.androidx.test.junit)
    androidTestImplementation(libs.androidx.compose.test)
}
