from setuptools import setup, find_packages

setup(
    name="pyaisloader",
    version="0.0.1",
    entry_points={
        "console_scripts": [
            "pyaisloader=pyaisloader.main:main",
        ],
    },
    packages=find_packages(include=["pyaisloader", "pyaisloader.*"]),
    install_requires=[
        "alive_progress>=3.1.5",
        "colorama>=0.4.6",
        "humanfriendly>=10.0",
        "pendulum>=2.1.2",
        "tabulate>=0.9.0",
        "torch>=2.1.1",
        "torchdata>=0.7.1",
    ],
)
