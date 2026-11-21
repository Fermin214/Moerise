import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.serialization)
    alias(libs.plugins.ksp)
    alias(libs.plugins.room)
    alias(libs.plugins.compose.compiler)
}
android {
    namespace = "io.github.fermin214.moerise"
    compileSdk = 36
    defaultConfig {
        applicationId = "io.github.fermin214.moerise"
        minSdk = providers.gradleProperty("moerise.minSdk").get().toInt()
        targetSdk = 36
        versionCode = providers.gradleProperty("moeriseVersionCode").orElse("2").get().toInt()
            .also { require(it > 0) { "moeriseVersionCode 必须为正整数" } }
        versionName = "0.2.0"
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
    testOptions.unitTests.isIncludeAndroidResources = true
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
    implementation(project(":core:model"))
    implementation(project(":core:sync"))
    implementation(project(":core:markdown"))
    implementation(libs.androidx.compose.foundation)
    implementation(libs.androidx.compose.material3)
    implementation(libs.markdown)
    implementation(libs.sqlite.framework)
    implementation(libs.coroutines.core)
    implementation(libs.serialization.json)
    implementation(libs.ktor.client.core)
    implementation(libs.ktor.client.okhttp)
    ksp(libs.room.compiler)
    testImplementation(kotlin("test"))
    testImplementation(libs.ktor.client.mock)
    testImplementation(libs.robolectric)
    testImplementation(libs.androidx.test.core)
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.core)
    implementation(libs.androidx.viewmodel)
    implementation(libs.room.runtime)
    implementation(libs.androidx.tracing)
    implementation(libs.androidx.profileinstaller)
    androidTestImplementation(libs.androidx.test.runner)
    androidTestImplementation(libs.androidx.test.junit)
    androidTestImplementation(libs.androidx.compose.test)
    androidTestImplementation(libs.androidx.compose.foundation)
    androidTestImplementation(libs.ktor.client.core)
    androidTestImplementation(libs.ktor.client.mock)
    androidTestImplementation(libs.serialization.json)
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

room { schemaDirectory("$projectDir/schemas") }
tasks.withType<Test>().configureEach {
    maxHeapSize = "512m"
    jvmArgs("-XX:ActiveProcessorCount=2")
    systemProperty("robolectric.dependency.repo.url", "https://repo.maven.apache.org/maven2")
    testLogging.events("passed", "failed", "skipped")
    if (!name.contains("Integration")) exclude("**/CoreIntegrationTest*", "**/StarMapIntegrationTest*")
}
for ((taskName, testClass) in listOf("coreIntegrationTest" to "CoreIntegrationTest", "starsIntegrationTest" to "StarMapIntegrationTest")) {
    tasks.register<Test>(taskName) {
        dependsOn("compileDebugUnitTestKotlin")
        val androidUnitTest = tasks.named<Test>("testDebugUnitTest").get()
        testClassesDirs = androidUnitTest.testClassesDirs
        classpath = androidUnitTest.classpath
        systemProperties(androidUnitTest.systemProperties)
        include("**/$testClass*")
        outputs.upToDateWhen { false }
    }
}
