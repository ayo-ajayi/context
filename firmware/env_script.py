Import("env")

def read_build_flags():
    with open('build_flags.ini', 'r') as file:
        flags = file.read().splitlines()
        return [flag.strip() for flag in flags if flag.strip()]

build_flags = read_build_flags()
env.Append(CPPDEFINES=build_flags)
