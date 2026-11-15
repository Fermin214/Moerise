plugins {
    alias(libs.plugins.android.test)
    alias(libs.plugins.kotlin.android)
}

android {
    namespace = "io.github.fermin214.moerise.benchmark.runner"
    compileSdk = 36
    targetProjectPath = ":androidApp"
    defaultConfig {
        minSdk = 28
        targetSdk = 36
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }
    buildTypes {
        create("benchmark") {
            isDebuggable = true
            signingConfig = signingConfigs.getByName("debug")
            matchingFallbacks += "release"
        }
        create("profile") {
            isDebuggable = true
            signingConfig = signingConfigs.getByName("debug")
            matchingFallbacks += "release"
        }
    }
    experimentalProperties["android.experimental.self-instrumenting"] = true
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}
kotlin { compilerOptions.jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17) }
androidComponents {
    beforeVariants(selector().all()) { it.enable = it.buildType in setOf("benchmark", "profile") }
}
dependencies {
    val perfettoVersion = "1.0.1"
    constraints {
        implementation("androidx.tracing:tracing-perfetto:$perfettoVersion")
        implementation("androidx.tracing:tracing-perfetto-binary:$perfettoVersion")
        implementation("androidx.tracing:tracing-perfetto-handshake:$perfettoVersion")
    }
    implementation(libs.androidx.benchmark)
    implementation(libs.androidx.uiautomator)
    implementation(libs.androidx.test.junit)
    implementation(libs.androidx.test.runner)
}
