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
    sourceSets.getByName("main").assets.srcDir(layout.buildDirectory.dir("generated/traceProcessor"))
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}
val prepareTraceProcessor by tasks.registering(Exec::class) {
    val script = rootProject.file("../scripts/android/trace-processor.py")
    val output = layout.buildDirectory.dir("generated/traceProcessor")
    inputs.file(script)
    outputs.dir(output)
    val python = providers.environmentVariable("MOERISE_ANDROID_PYTHON")
        .orElse(if (System.getProperty("os.name").startsWith("Windows")) "python" else "python3")
    commandLine(python.get(), script.absolutePath, output.get().asFile.absolutePath)
}
tasks.named("preBuild") { dependsOn(prepareTraceProcessor) }
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
