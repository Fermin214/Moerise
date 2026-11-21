plugins {
    alias(libs.plugins.kotlin.jvm)
}
kotlin { jvmToolchain(17) }
dependencies {
    implementation(libs.markdown.parser)
    testImplementation(kotlin("test"))
}
