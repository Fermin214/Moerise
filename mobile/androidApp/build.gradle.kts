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
        minSdk = providers.gradleProperty("moerise.minSdk").get().toInt()
        targetSdk = 36
        versionCode = providers.gradleProperty("moeriseVersionCode").orElse("1").get().toInt()
            .also { require(it > 0) { "moeriseVersionCode 必须为正整数" } }
        versionName = "0.1.0"
        manifestPlaceholders["appLabel"] = "Moerise"
        ndk { abiFilters += listOf("arm64-v8a", "x86_64") }
        buildConfigField("boolean", "ALLOW_LOOPBACK_HTTP", "false")
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }
    buildFeatures { compose = true; buildConfig = true }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    buildTypes {
        debug {
            applicationIdSuffix = ".dev"
            manifestPlaceholders["appLabel"] = "Moerise Dev"
            buildConfigField("boolean", "ALLOW_LOOPBACK_HTTP", "true")
        }
        create("fixture") {
            initWith(getByName("debug"))
            applicationIdSuffix = ".fixture"
            manifestPlaceholders["appLabel"] = "Moerise Fixture"
            matchingFallbacks += "debug"
        }
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
        create("benchmark") {
            initWith(getByName("release"))
            applicationIdSuffix = ".benchmark"
            manifestPlaceholders["appLabel"] = "Moerise Benchmark"
            signingConfig = signingConfigs.getByName("debug")
            matchingFallbacks += "release"
            buildConfigField("boolean", "ALLOW_LOOPBACK_HTTP", "true")
        }
        create("profile") {
            initWith(getByName("benchmark"))
            isMinifyEnabled = false
            isShrinkResources = false
        }
    }
    testBuildType = "fixture"
    sourceSets.getByName("fixture").res.srcDir("src/debug/res")
    sourceSets.getByName("benchmark").res.srcDir("src/debug/res")
    sourceSets.getByName("profile").apply {
        res.srcDir("src/debug/res")
        java.srcDir("src/benchmark/kotlin")
        manifest.srcFile("src/benchmark/AndroidManifest.xml")
    }
}
kotlin { compilerOptions.jvmTarget.set(JvmTarget.JVM_17) }
dependencies {
    implementation(project(":shared"))
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.core)
    implementation(libs.androidx.viewmodel)
    implementation(libs.room.runtime)
    implementation(libs.androidx.tracing)
    implementation(libs.androidx.profileinstaller)
    androidTestImplementation(libs.androidx.test.runner)
    androidTestImplementation(libs.androidx.test.junit)
    androidTestImplementation(libs.androidx.compose.test)
    androidTestImplementation(libs.ktor.client.core)
    listOf("benchmarkImplementation", "profileImplementation").forEach { configuration ->
        add(configuration, libs.androidx.compose.foundation)
        add(configuration, libs.androidx.compose.material3)
        add(configuration, libs.serialization.json)
    }
}

tasks.register("exportReleaseDependencies") {
    val report = layout.buildDirectory.file("reports/release-dependencies.tsv")
    outputs.file(report)
    outputs.upToDateWhen { false }
    doLast {
        val artifacts = configurations.getByName("releaseRuntimeClasspath").incoming.artifactView {
            componentFilter { it is org.gradle.api.artifacts.component.ModuleComponentIdentifier }
        }.artifacts.artifacts
        report.get().asFile.apply {
            parentFile.mkdirs()
            writeText(artifacts.map {
                val module = it.id.componentIdentifier as org.gradle.api.artifacts.component.ModuleComponentIdentifier
                "${module.group}:${module.module}:${module.version}\t${it.file.absolutePath}"
            }.distinct().sorted().joinToString("\n", postfix = "\n"))
        }
    }
}
